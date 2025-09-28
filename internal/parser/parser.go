package parser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	mailpkg "net/mail"
	"regexp"
	"strings"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/textproto"

	"monitor-imap-webhook/internal/config"
)

func init() {
	// charset package already sets message.CharsetReader; extra enc alias examples skipped
}

// Message holds parsed essential fields.
type Message struct {
	UID             uint32
	Subject         string
	From            string
	Date            string
	Body            string
	RawHTML         string           // 原始 HTML (若存在且启用)
	Blocks          []map[string]any // 结构化 blocks (若启用)
	HasAttachments  bool             // 是否存在附件
	AttachmentNames []string         // 附件文件名列表
}

// FetchAndParse retrieves a message by UID and parses it.
// FetchAndParse retrieves a message by UID and parses it.
func FetchAndParse(exec func(ctx context.Context, op string, fn func(c *client.Client) error) error, cfg *config.Config, uid uint32) (*Message, error) {
	// We'll store parsed message in outer scope
	var parsed *Message
	// use background context for now (could pass a caller ctx)
	ctx := context.Background()
	err := exec(ctx, "fetch", func(c *client.Client) error {
		seqset := new(imap.SeqSet)
		seqset.AddNum(uid)
		section := &imap.BodySectionName{}
		items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, imap.FetchBodyStructure, imap.FetchFlags, section.FetchItem()}
		ch := make(chan *imap.Message, 1)
		if err := c.UidFetch(seqset, items, ch); err != nil {
			return fmt.Errorf("uid fetch: %w", err)
		}
		msg := <-ch
		if msg == nil {
			return errors.New("message not found")
		}
		var raw []byte
		if lit := msg.GetBody(section); lit != nil {
			buf := new(bytes.Buffer)
			io.Copy(buf, lit)
			raw = buf.Bytes()
		}
		p, perr := parseRaw(raw, msg, cfg)
		if perr != nil {
			return perr
		}
		p.UID = msg.Uid
		parsed = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseRaw(raw []byte, im *imap.Message, cfg *config.Config) (*Message, error) {
	email, err := mailpkg.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	hdr := email.Header

	subj := decodeHeader(hdr.Get("Subject"))
	fromRaw := hdr.Get("From")
	from := fromRaw
	if addr, err2 := mailpkg.ParseAddress(fromRaw); err2 == nil {
		name := addr.Name
		if name != "" {
			name = decodeHeader(name)
		}
		if name != "" {
			// 如果显示名与地址相同（大小写忽略），避免冗余格式
			if strings.EqualFold(name, addr.Address) {
				from = addr.Address
			} else {
				from = fmt.Sprintf("%s <%s>", name, addr.Address)
			}
		} else {
			from = addr.Address
		}
	}
	date := hdr.Get("Date")

	body, rawHTML, err := extractBody(email, cfg)
	if err != nil {
		log.Printf("extract body error: %v", err)
	}
	msg := &Message{Subject: subj, From: from, Date: date, Body: body}
	// 附件检测（基于 imap.Message BodyStructure）
	if im != nil && im.BodyStructure != nil {
		var ordered []string
		seen := make(map[string]struct{})
		var walk func(bs *imap.BodyStructure)
		walk = func(bs *imap.BodyStructure) {
			if bs == nil {
				return
			}
			if len(bs.Parts) > 0 { // multipart 递归
				for _, p := range bs.Parts {
					walk(p)
				}
				return
			}
			// 叶子 part，判断是否附件
			disp := strings.ToLower(bs.Disposition)
			if disp == "attachment" || disp == "inline" {
				// 跳过内联图片（若配置启用）
				if cfg.SkipInlineImages && disp == "inline" && strings.EqualFold(bs.MIMEType, "image") {
					return
				}
				filename := firstNonEmpty(bs.Params["name"], bs.Params["filename"], bs.DispositionParams["filename"], bs.DispositionParams["name"])
				if filename == "" {
					return
				}
				decodedName := decodeHeader(filename)
				candidate := strings.TrimSpace(decodedName)
				if candidate == "" {
					candidate = filename
				}
				if _, ok := seen[candidate]; ok {
					return
				}
				seen[candidate] = struct{}{}
				ordered = append(ordered, candidate)
			}
		}
		walk(im.BodyStructure)
		if len(ordered) > 0 {
			msg.HasAttachments = true
			msg.AttachmentNames = ordered
		}
	}
	if cfg.IncludeRawHTML {
		msg.RawHTML = rawHTML
	}
	if cfg.EnableBlocks && rawHTML != "" {
		msg.Blocks = buildBlocksFromHTML(rawHTML, body)
	}
	return msg, nil
}

func decodeHeader(v string) string {
	if v == "" {
		return v
	}
	// Use mime.WordDecoder
	dec := new(mime.WordDecoder)
	dec.CharsetReader = charset.Reader
	res, err := dec.DecodeHeader(v)
	if err != nil {
		return v
	}
	return res
}

// extractBody 返回 (纯文本, 原始HTML)
func extractBody(m *mailpkg.Message, cfg *config.Config) (string, string, error) {
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		// fallback treat as plain
		b, _ := io.ReadAll(m.Body)
		decoded := decodeTransferIfNeeded(b, m.Header.Get("Content-Transfer-Encoding"))
		return limitText(string(decoded)), "", nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := textproto.NewMultipartReader(m.Body, params["boundary"])
		var plain, html string
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", "", err
			}
			ct := p.Header.Get("Content-Type")
			cte := p.Header.Get("Content-Transfer-Encoding")
			if strings.HasPrefix(ct, "text/plain") && plain == "" {
				b, _ := io.ReadAll(p)
				b = decodeTransferIfNeeded(b, cte)
				plain = string(b)
			}
			if strings.HasPrefix(ct, "text/html") && html == "" {
				b, _ := io.ReadAll(p)
				b = decodeTransferIfNeeded(b, cte)
				original := string(b)
				cleaned := removeStyleTags(original)
				html = htmlToText(cleaned, cfg.HTMLToTextMode)
				// 保留原始 HTML 以便后续 blocks 构建
				if cfg.IncludeRawHTML || cfg.EnableBlocks {
					// original HTML 留给上层 parseRaw 设置 RawHTML (通过返回第二个值)
				}
			}
			if plain != "" && html != "" {
				break
			}
		}
		if plain != "" {
			return limitText(plain), html, nil
		}
		if html != "" {
			return limitText(html), html, nil
		}
		return "", html, nil
	}
	b, _ := io.ReadAll(m.Body)
	cte := m.Header.Get("Content-Transfer-Encoding")
	b = decodeTransferIfNeeded(b, cte)
	if strings.HasPrefix(mediaType, "text/html") {
		h := string(b)
		clean := removeStyleTags(h)
		return limitText(htmlToText(clean, cfg.HTMLToTextMode)), h, nil
	}
	return limitText(string(b)), "", nil
}

func htmlToText(s, mode string) string {
	if mode == "none" {
		return s
	}
	// very naive stripping
	replacer := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<p>", "\n", "</p>", "\n")
	s = replacer.Replace(s)
	// remove tags
	var buf strings.Builder
	inTag := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			buf.WriteByte(c)
		}
	}
	text := buf.String()
	// decode common entities minimal
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	text = strings.Join(lines, func() string {
		if mode == "preserve-line" {
			return "\n"
		}
		return " "
	}())
	// collapse spaces
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func limitText(s string) string {
	if len(s) > 20000 {
		return s[:20000] + "...<truncated>"
	}
	return s
}

// decodeTransferIfNeeded 解码 quoted-printable / base64
func decodeTransferIfNeeded(data []byte, cte string) []byte {
	cte = strings.ToLower(strings.TrimSpace(cte))
	switch cte {
	case "quoted-printable":
		res, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err == nil {
			return res
		}
	case "base64":
		dec := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
		n, err := base64.StdEncoding.Decode(dec, bytes.TrimSpace(data))
		if err == nil {
			return dec[:n]
		}
	}
	return data
}

// removeStyleTags 去掉 <style>...</style>
var styleTagRe = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
var scriptTagRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
var msoCondRe = regexp.MustCompile(`(?is)<!\[if.*?<!\[endif\]>`)

func removeStyleTags(html string) string {
	res := styleTagRe.ReplaceAllString(html, "")
	res = scriptTagRe.ReplaceAllString(res, "")
	res = htmlCommentRe.ReplaceAllString(res, "")
	res = msoCondRe.ReplaceAllString(res, "")
	return res
}

// buildBlocksFromHTML 生成一个非常轻量级的 blocks AST；不依赖外部 HTML 解析库，
// 仅通过正则/分割识别常见结构。为了控制复杂度，这里采用启发式：
// - <h1..h6> -> {type: "heading", level: n, text}
// - <p> 或被两次换行分隔的文本 -> {type: "paragraph", text}
// - <ul><li> / <ol><li> -> {type: "list", ordered: bool, items: []}
// - <blockquote> -> {type: "blockquote", text}
// - <pre><code> 或 <code> 块 -> {type: "code", text}
// 其余忽略。退化情况下 fallback 使用纯文本按空行分段。
func buildBlocksFromHTML(html, plain string) []map[string]any {
	if html == "" {
		return buildParagraphBlocksFromPlain(plain)
	}
	// 预处理：统一小写标签（不处理内容大小写），简化匹配
	l := strings.ToLower(html)
	blocks := []map[string]any{}

	// 提取并移除 code 块
	codeRe := regexp.MustCompile(`(?s)<pre[^>]*><code[^>]*>(.*?)</code></pre>`)
	for {
		m := codeRe.FindStringSubmatch(l)
		if m == nil {
			break
		}
		text := htmlStripTags(m[1])
		blocks = append(blocks, map[string]any{"type": "code", "text": strings.TrimSpace(text)})
		l = strings.Replace(l, m[0], "\n", 1)
	}

	// 头部
	headingRe := regexp.MustCompile(`(?s)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	foundHeadings := headingRe.FindAllStringSubmatch(l, -1)
	for _, h := range foundHeadings {
		lvl := h[1]
		text := htmlStripTags(h[2])
		blocks = append(blocks, map[string]any{"type": "heading", "level": lvl, "text": strings.TrimSpace(text)})
		l = strings.Replace(l, h[0], "\n", 1)
	}

	// 列表 (unordered)
	ulRe := regexp.MustCompile(`(?s)<ul[^>]*>(.*?)</ul>`)
	for {
		m := ulRe.FindStringSubmatch(l)
		if m == nil {
			break
		}
		items := extractListItems(m[1])
		if len(items) > 0 {
			blocks = append(blocks, map[string]any{"type": "list", "ordered": false, "items": items})
		}
		l = strings.Replace(l, m[0], "\n", 1)
	}
	// 列表 (ordered)
	olRe := regexp.MustCompile(`(?s)<ol[^>]*>(.*?)</ol>`)
	for {
		m := olRe.FindStringSubmatch(l)
		if m == nil {
			break
		}
		items := extractListItems(m[1])
		if len(items) > 0 {
			blocks = append(blocks, map[string]any{"type": "list", "ordered": true, "items": items})
		}
		l = strings.Replace(l, m[0], "\n", 1)
	}

	// 引用
	quoteRe := regexp.MustCompile(`(?s)<blockquote[^>]*>(.*?)</blockquote>`)
	foundQuotes := quoteRe.FindAllStringSubmatch(l, -1)
	for _, q := range foundQuotes {
		text := htmlStripTags(q[1])
		blocks = append(blocks, map[string]any{"type": "blockquote", "text": strings.TrimSpace(text)})
		l = strings.Replace(l, q[0], "\n", 1)
	}

	// 剩余分段（<p> 或者按空行）
	pRe := regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)
	foundP := pRe.FindAllStringSubmatch(l, -1)
	if len(foundP) > 0 {
		for _, p := range foundP {
			text := strings.TrimSpace(htmlStripTags(p[1]))
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "paragraph", "text": text})
			}
		}
	} else {
		// fallback: 用 plain 文本按空行段落拆分
		blocks = append(blocks, buildParagraphBlocksFromPlain(plain)...)
	}

	return blocks
}

func extractListItems(section string) []string {
	liRe := regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	matches := liRe.FindAllStringSubmatch(section, -1)
	var items []string
	for _, m := range matches {
		text := strings.TrimSpace(htmlStripTags(m[1]))
		if text != "" {
			items = append(items, text)
		}
	}
	return items
}

func htmlStripTags(s string) string {
	var b strings.Builder
	inTag := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func buildParagraphBlocksFromPlain(plain string) []map[string]any {
	if plain == "" {
		return nil
	}
	parts := regexp.MustCompile(`\n{2,}`).Split(plain, -1)
	var blocks []map[string]any
	for _, p := range parts {
		pt := strings.TrimSpace(p)
		if pt == "" {
			continue
		}
		blocks = append(blocks, map[string]any{"type": "paragraph", "text": pt})
	}
	return blocks
}

// firstNonEmpty 返回首个非空字符串
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
