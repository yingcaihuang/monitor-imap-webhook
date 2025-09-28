package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"monitor-imap-webhook/internal/config"
)

type Payload struct {
	UID            uint32        `json:"uid"`
	Subject        string        `json:"subject"`
	From           string        `json:"from"`
	Date           string        `json:"date"`
	Body           string        `json:"body"`                 // 原始（已做 html->text 处理后的）纯文本
	BodyLines      []string      `json:"body_lines,omitempty"` // 拆分后的行（去除多余空行）
	Preview        string        `json:"preview"`              // 前 N 字符预览
	WordCount      int           `json:"word_count"`
	Mailbox        string        `json:"mailbox"`
	Timestamp      int64         `json:"timestamp"`
	RawHTML        string        `json:"raw_html,omitempty"` // 原始 HTML (可选)
	Blocks         []interface{} `json:"blocks,omitempty"`   // 结构化 AST blocks (可选)
	HasAttachments bool          `json:"has_attachments,omitempty"`
	Attachments    []string      `json:"attachments,omitempty"`
	AttachmentCount int          `json:"attachment_count,omitempty"`
}

type Sender struct {
	cfg *config.Config
	hc  *http.Client
}

func NewSender(cfg *config.Config) *Sender {
	return &Sender{cfg: cfg, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Sender) parseHeaders(raw string) http.Header {
	h := http.Header{}
	if raw == "" {
		return h
	}
	pairs := strings.Split(raw, ";")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		h.Add(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
	}
	return h
}

func (s *Sender) SendWithRetry(p Payload) error {
	data, _ := json.Marshal(p)
	headers := s.parseHeaders(s.cfg.WebhookHeader)
	backoff := s.cfg.RetryBaseBackoff
	for attempt := 0; attempt <= s.cfg.RetryMax; attempt++ {
		req, _ := http.NewRequest("POST", s.cfg.WebhookURL, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		for k, vals := range headers {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
		resp, err := s.hc.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if attempt == s.cfg.RetryMax {
			break
		}
		time.Sleep(backoff)
		backoff *= 2
	}
	return errors.New("webhook send failed after retries")
}

// BuildPayload 规范化并补充结构化字段（预览、行拆分、词数）。
func BuildPayload(msg *Payload, bodyLimit int) Payload {
	if msg == nil {
		return Payload{}
	}
	out := *msg
	// 先拆行，挑选第一条语义内容行作为 preview 来源
	var semanticFirst string
	for _, ln := range strings.Split(out.Body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		// 跳过可能是样式/模板噪音的行
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "@media") || strings.HasPrefix(low, "table ") || strings.Contains(low, "font-family") || strings.Contains(low, "{") {
			continue
		}
		semanticFirst = ln
		break
	}
	if semanticFirst == "" {
		// 若正文全空，尝试基于附件生成预览
		trimmedBody := strings.TrimSpace(strings.ReplaceAll(out.Body, "\n", " "))
		if trimmedBody == "" && out.HasAttachments && len(out.Attachments) > 0 {
			maxList := out.Attachments
			if len(maxList) > 5 { // 预览最多列出 5 个
				maxList = maxList[:5]
			}
			semanticFirst = "Attachments: " + strings.Join(maxList, ", ")
			if len(out.Attachments) > len(maxList) {
				semanticFirst += fmt.Sprintf(" (+%d more)", len(out.Attachments)-len(maxList))
			}
		} else {
			semanticFirst = trimmedBody
		}
	}
	previewLimit := 140
	if len(semanticFirst) > previewLimit {
		out.Preview = semanticFirst[:previewLimit] + "..."
	} else {
		out.Preview = semanticFirst
	}
	// 限制正文长度
	if bodyLimit > 0 && len(out.Body) > bodyLimit {
		out.Body = out.Body[:bodyLimit] + "...<truncated>"
	}
	// 拆分行与清洗
	var lines []string
	for _, ln := range strings.Split(out.Body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		lines = append(lines, ln)
	}
	out.BodyLines = lines
	// 改进词数统计：英文按空白；CJK 统计连续汉字 rune
	if out.Body != "" {
		out.WordCount = mixedWordCount(out.Body)
	}
	if out.Timestamp == 0 {
		out.Timestamp = time.Now().Unix()
	}
	return out
}

// mixedWordCount: 英文按空白拆分；CJK(汉字/日文/韩文块)按单字符计；数字按整体；忽略标点。
func mixedWordCount(s string) int {
	var count int
	current := strings.Builder{}
	isASCIIWord := func(r rune) bool {
		return r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}
	isCJK := func(r rune) bool { return r >= 0x4E00 && r <= 0x9FFF }
	flush := func() {
		if current.Len() > 0 {
			count++
			current.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isASCIIWord(r):
			current.WriteRune(r)
		case isCJK(r):
			flush()
			count++
		default:
			flush()
		}
	}
	flush()
	return count
}

func Example() {
	fmt.Println("webhook sender ready")
}
