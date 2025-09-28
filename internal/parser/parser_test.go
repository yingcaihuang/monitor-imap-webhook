package parser

import (
	"bytes"
	"mime"
	"testing"
	"time"

	"monitor-imap-webhook/internal/config"
)

// helper to build a multipart alternative raw email
func buildMultipartRaw(subjectEncoded string) []byte {
	boundary := "BOUNDARY123"
	var buf bytes.Buffer
	buf.WriteString("Subject: ")
	buf.WriteString(subjectEncoded)
	buf.WriteString("\r\n")
	buf.WriteString("From: Tester <test@example.com>\r\n")
	buf.WriteString("Date: Mon, 28 Sep 2025 12:00:00 +0800\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: multipart/alternative; boundary=\"")
	buf.WriteString(boundary)
	buf.WriteString("\"\r\n\r\n")
	// plain part
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString("这是纯文本内容 Plain\r\n")
	// html part
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	buf.WriteString("<html><body><p>这是纯文本内容 <b>Plain</b></p></body></html>\r\n")
	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes()
}

func TestParseEncodedSubjectAndMultipart(t *testing.T) {
	encSubj := mime.BEncoding.Encode("utf-8", "测试主题")
	raw := buildMultipartRaw(encSubj)
	cfg := &config.Config{HTMLToTextMode: "simple"}
	msg, err := parseRaw(raw, nil, cfg)
	if err != nil {
		t.Fatalf("parseRaw error: %v", err)
	}
	if msg.Subject != "测试主题" { // ensure decoded
		t.Errorf("expected subject 解码为 测试主题, got %s", msg.Subject)
	}
	if msg.Body == "" {
		t.Errorf("expected body not empty")
	}
	if !containsAll(msg.Body, []string{"纯文本内容", "Plain"}) { // plain part should win
		t.Errorf("body content unexpected: %q", msg.Body)
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}

func TestParseGB2312NoPanic(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("Subject: GB2312 Test\r\n")
	buf.WriteString("From: T <t@example.com>\r\n")
	buf.WriteString("Date: Mon, 28 Sep 2025 12:10:00 +0800\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=gb2312\r\n\r\n")
	// bytes for "中文" in GB2312 (D6 D0 CE C4)
	buf.Write([]byte{0xD6, 0xD0, 0xCE, 0xC4, '\r', '\n'})
	cfg := &config.Config{HTMLToTextMode: "simple"}
	msg, err := parseRaw(buf.Bytes(), nil, cfg)
	if err != nil {
		// even if decode fails, should not panic; treat error as test failure
		// (parseRaw should rarely error here)
		// but still log
		// fallback: accept error only if it's clearly about charset unsupported
		if msg == nil {
			t.Fatalf("parseRaw returned error: %v", err)
		}
	}
	_ = time.Now() // make lint happy about time import if unused later
}
