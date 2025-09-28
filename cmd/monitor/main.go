package main

import (
	"context"
	"log"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"monitor-imap-webhook/internal/config"
	"monitor-imap-webhook/internal/imapclient"
	"monitor-imap-webhook/internal/parser"
	"monitor-imap-webhook/internal/webhook"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}
	log.Printf("启动: host=%s port=%d mailbox=%s webhook=%s", cfg.IMAPHost, cfg.IMAPPort, cfg.Mailbox, cfg.WebhookURL)

	cl := imapclient.New(cfg)
	events := make(chan imapclient.Event, 50)
	sender := webhook.NewSender(cfg)

	go func() {
		if err := cl.IdleLoop(ctx, events); err != nil && ctx.Err() == nil {
			log.Printf("IdleLoop 退出: %v", err)
		}
	}()

	go func() {
		transientRe := regexp.MustCompile(`(?i)(short write|timeout|temporarily|reset|closed)`) // 简单匹配
		for ev := range events {
			var msg *parser.Message
			var perr error
			maxFetchRetry := 2
			for attempt := 0; attempt <= maxFetchRetry; attempt++ {
				msg, perr = parser.FetchAndParse(cl.Exec, cfg, ev.UID)
				if perr == nil {
					break
				}
				if !transientRe.MatchString(perr.Error()) { // 非瞬时错误不再重试
					break
				}
				if cfg.Debug {
					log.Printf("fetch transient error uid=%d attempt=%d err=%v", ev.UID, attempt, perr)
				}
				time.Sleep(150 * time.Millisecond)
			}
			if perr != nil {
				log.Printf("解析邮件失败 UID=%d: %v", ev.UID, perr)
				cl.EndProcess()
				continue
			}
			base := webhook.Payload{UID: msg.UID, Subject: msg.Subject, From: msg.From, Date: msg.Date, Body: msg.Body, Mailbox: cfg.Mailbox, Timestamp: time.Now().Unix()}
			if msg.HasAttachments {
				base.HasAttachments = true
				base.Attachments = msg.AttachmentNames
			}
			if cfg.IncludeRawHTML && msg.RawHTML != "" {
				base.RawHTML = msg.RawHTML
			}
			if cfg.EnableBlocks && len(msg.Blocks) > 0 {
				// convert []map[string]any to []interface{}
				for _, b := range msg.Blocks {
					base.Blocks = append(base.Blocks, b)
				}
			}
			payload := webhook.BuildPayload(&base, cfg.FetchBodySize)
			if err := sender.SendWithRetry(payload); err != nil {
				log.Printf("Webhook 发送失败 UID=%d: %v", ev.UID, err)
			} else {
				log.Printf("Webhook 已发送 UID=%d 主题=%s", ev.UID, truncate(msg.Subject, 60))
			}
			cl.EndProcess()
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	_ = cl.Close()
	time.Sleep(200 * time.Millisecond)
}

// getRawClient: 暂时通过类型断言访问内部字段（可以改为导出方法）。
// 为避免暴露内部实现，后续可以在 imapclient 包添加一个 ExportUnderlying() 方法。
// 这里先写一个占位函数，需要你在 imapclient 包中补一个方法。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
