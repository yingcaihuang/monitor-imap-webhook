package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds application configuration.
type Config struct {
	IMAPHost           string        `yaml:"imap_host"`
	IMAPPort           int           `yaml:"imap_port"`
	Username           string        `yaml:"username"`
	Password           string        `yaml:"password"`
	Mailbox            string        `yaml:"mailbox"`
	UseTLS             bool          `yaml:"tls"`
	StartTLS           bool          `yaml:"starttls"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	CheckInterval      time.Duration `yaml:"interval"`
	DrainTimeout       time.Duration `yaml:"drain_timeout"`
	WebhookURL         string        `yaml:"webhook"`
	WebhookHeader      string        `yaml:"webhook_header"`
	FetchBodySize      int           `yaml:"fetch_body_bytes"`
	RetryMax           int           `yaml:"retry_max"`
	RetryBaseBackoff   time.Duration `yaml:"retry_backoff"`
	HTMLToTextMode     string        `yaml:"html2text"`          // simple | preserve-line | none
	IncludeRawHTML     bool          `yaml:"raw_html"`           // 是否在 payload 中包含原始 HTML（若存在）
	EnableBlocks       bool          `yaml:"enable_blocks"`      // 是否基于 HTML 解析结构化 blocks
	SkipInlineImages   bool          `yaml:"skip_inline_images"` // 是否忽略 disposition=inline 且 content-type image/* 的附件
	Debug              bool          `yaml:"debug"`
}

// pointer wrapper for YAML detection of presence
type fileConfig struct {
	IMAPHost           *string        `yaml:"imap_host"`
	IMAPPort           *int           `yaml:"imap_port"`
	Username           *string        `yaml:"username"`
	Password           *string        `yaml:"password"`
	Mailbox            *string        `yaml:"mailbox"`
	UseTLS             *bool          `yaml:"tls"`
	StartTLS           *bool          `yaml:"starttls"`
	InsecureSkipVerify *bool          `yaml:"insecure_skip_verify"`
	CheckInterval      *time.Duration `yaml:"interval"`
	DrainTimeout       *time.Duration `yaml:"drain_timeout"`
	WebhookURL         *string        `yaml:"webhook"`
	WebhookHeader      *string        `yaml:"webhook_header"`
	FetchBodySize      *int           `yaml:"fetch_body_bytes"`
	RetryMax           *int           `yaml:"retry_max"`
	RetryBaseBackoff   *time.Duration `yaml:"retry_backoff"`
	HTMLToTextMode     *string        `yaml:"html2text"`
	IncludeRawHTML     *bool          `yaml:"raw_html"`
	EnableBlocks       *bool          `yaml:"enable_blocks"`
	SkipInlineImages   *bool          `yaml:"skip_inline_images"`
	Debug              *bool          `yaml:"debug"`
}

// custom flag value types to know if user explicitly set
type stringFlag struct {
	val string
	set bool
}

func (s *stringFlag) String() string     { return s.val }
func (s *stringFlag) Set(v string) error { s.val = v; s.set = true; return nil }

type intFlag struct {
	val int
	set bool
}

func (i *intFlag) String() string { return fmt.Sprintf("%d", i.val) }
func (i *intFlag) Set(v string) error {
	var tmp int
	if _, err := fmt.Sscanf(v, "%d", &tmp); err != nil {
		return err
	}
	i.val = tmp
	i.set = true
	return nil
}

type boolFlag struct {
	val bool
	set bool
}

func (b *boolFlag) String() string {
	if b.val {
		return "true"
	}
	return "false"
}
func (b *boolFlag) Set(v string) error {
	if v == "1" || v == "true" || v == "TRUE" || v == "yes" {
		b.val = true
	} else {
		b.val = false
	}
	b.set = true
	return nil
}

type durationFlag struct {
	val time.Duration
	set bool
}

func (d *durationFlag) String() string { return d.val.String() }
func (d *durationFlag) Set(v string) error {
	dur, err := time.ParseDuration(v)
	if err != nil {
		return err
	}
	d.val = dur
	d.set = true
	return nil
}

func Load() (*Config, error) {
	// 1. 内部默认值
	cfg := &Config{
		IMAPPort:         993,
		Mailbox:          "INBOX",
		UseTLS:           true,
		FetchBodySize:    200 * 1024,
		RetryMax:         5,
		RetryBaseBackoff: 1 * time.Second,
		HTMLToTextMode:   "simple",
		CheckInterval:    30 * time.Second,
		DrainTimeout:     3 * time.Second,
		IncludeRawHTML:   false,
		EnableBlocks:     false,
		SkipInlineImages: false,
	}

	// 2. 环境变量覆盖 (若存在)
	if v, ok := os.LookupEnv("IMAP_HOST"); ok {
		cfg.IMAPHost = v
	}
	if v, ok := os.LookupEnv("IMAP_PORT"); ok {
		var n int
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.IMAPPort = n
		}
	}
	if v, ok := os.LookupEnv("IMAP_USERNAME"); ok {
		cfg.Username = v
	}
	if v, ok := os.LookupEnv("IMAP_PASSWORD"); ok {
		cfg.Password = v
	}
	if v, ok := os.LookupEnv("IMAP_MAILBOX"); ok {
		cfg.Mailbox = v
	}
	if v, ok := os.LookupEnv("IMAP_TLS"); ok {
		cfg.UseTLS = parseBool(v)
	}
	if v, ok := os.LookupEnv("IMAP_STARTTLS"); ok {
		cfg.StartTLS = parseBool(v)
	}
	if v, ok := os.LookupEnv("IMAP_INSECURE_SKIP_VERIFY"); ok {
		cfg.InsecureSkipVerify = parseBool(v)
	}
	if v, ok := os.LookupEnv("IMAP_INTERVAL"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.CheckInterval = d
		}
	}
	if v, ok := os.LookupEnv("DRAIN_TIMEOUT"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DrainTimeout = d
		}
	}
	if v, ok := os.LookupEnv("WEBHOOK_URL"); ok {
		cfg.WebhookURL = v
	}
	if v, ok := os.LookupEnv("WEBHOOK_HEADER"); ok {
		cfg.WebhookHeader = v
	}
	if v, ok := os.LookupEnv("FETCH_BODY_BYTES"); ok {
		var n int
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.FetchBodySize = n
		}
	}
	if v, ok := os.LookupEnv("RETRY_MAX"); ok {
		var n int
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			cfg.RetryMax = n
		}
	}
	if v, ok := os.LookupEnv("RETRY_BACKOFF"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RetryBaseBackoff = d
		}
	}
	if v, ok := os.LookupEnv("HTML2TEXT_MODE"); ok {
		cfg.HTMLToTextMode = v
	}
	if v, ok := os.LookupEnv("RAW_HTML"); ok {
		cfg.IncludeRawHTML = parseBool(v)
	}
	if v, ok := os.LookupEnv("ENABLE_BLOCKS"); ok {
		cfg.EnableBlocks = parseBool(v)
	}
	if v, ok := os.LookupEnv("SKIP_INLINE_IMAGES"); ok {
		cfg.SkipInlineImages = parseBool(v)
	}
	if v, ok := os.LookupEnv("DEBUG"); ok {
		cfg.Debug = parseBool(v)
	}

	// 3. 预解析 flags 仅获取 --config
	var configPath string
	pre := flag.NewFlagSet("pre", flag.ContinueOnError)
	pre.StringVar(&configPath, "config", "", "配置文件路径 (YAML)")
	_ = pre.Parse(os.Args[1:]) // 忽略错误, 由主解析处理

	// 4. 若存在配置文件, 解析并覆盖 (高于 env 低于显式 flag)
	if configPath != "" {
		if err := mergeFile(configPath, cfg); err != nil {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	}

	// 5. 定义主 flag (跟踪是否显式提供)
	sfHost := &stringFlag{val: cfg.IMAPHost}
	if cfg.IMAPHost == "" {
		sfHost.val = ""
	}
	flag.Var(sfHost, "imap-host", "IMAP 服务器主机名")
	ifPort := &intFlag{val: cfg.IMAPPort}
	flag.Var(ifPort, "imap-port", "IMAP 服务器端口")
	sfUser := &stringFlag{val: cfg.Username}
	flag.Var(sfUser, "username", "IMAP 用户名")
	sfPass := &stringFlag{val: cfg.Password}
	flag.Var(sfPass, "password", "IMAP 密码 (或应用专用密码)")
	sfMailbox := &stringFlag{val: cfg.Mailbox}
	flag.Var(sfMailbox, "mailbox", "监控的邮箱/文件夹")
	bfTLS := &boolFlag{val: cfg.UseTLS}
	flag.Var(bfTLS, "tls", "直接 TLS 连接 (993)")
	bfStartTLS := &boolFlag{val: cfg.StartTLS}
	flag.Var(bfStartTLS, "starttls", "先普通连接再 STARTTLS")
	bfSkip := &boolFlag{val: cfg.InsecureSkipVerify}
	flag.Var(bfSkip, "insecure-skip-verify", "跳过 TLS 证书验证 (自签名测试环境，不建议生产启用)")
	dfInterval := &durationFlag{val: cfg.CheckInterval}
	flag.Var(dfInterval, "interval", "轮询间隔(无 IDLE 时)")
	dfDrain := &durationFlag{val: cfg.DrainTimeout}
	flag.Var(dfDrain, "drain-timeout", "新邮件 UID 推送后等待正文抓取完成的最大时间(避免 IDLE/FETCH 并发)")
	sfWebhook := &stringFlag{val: cfg.WebhookURL}
	flag.Var(sfWebhook, "webhook", "Webhook 接收地址")
	sfHeader := &stringFlag{val: cfg.WebhookHeader}
	flag.Var(sfHeader, "webhook-header", "额外 Header, 例如: X-Token=abc123")
	ifFetch := &intFlag{val: cfg.FetchBodySize}
	flag.Var(ifFetch, "fetch-body-bytes", "单次抓取正文最大字节数 (截断保护)")
	ifRetryMax := &intFlag{val: cfg.RetryMax}
	flag.Var(ifRetryMax, "retry-max", "Webhook 重试最大次数")
	dfRetryBackoff := &durationFlag{val: cfg.RetryBaseBackoff}
	flag.Var(dfRetryBackoff, "retry-backoff", "Webhook 重试初始退避时间")
	sfHTML := &stringFlag{val: cfg.HTMLToTextMode}
	flag.Var(sfHTML, "html2text", "HTML 转纯文本策略: simple|preserve-line|none")
	bfRaw := &boolFlag{val: cfg.IncludeRawHTML}
	flag.Var(bfRaw, "raw-html", "在 Webhook Payload 中包含原始 HTML 内容 (可能较大)")
	bfBlocks := &boolFlag{val: cfg.EnableBlocks}
	flag.Var(bfBlocks, "enable-blocks", "基于 HTML 解析结构化 blocks (实验特性)")
	bfSkipInline := &boolFlag{val: cfg.SkipInlineImages}
	flag.Var(bfSkipInline, "skip-inline-images", "忽略 disposition=inline 且 content-type image/* 的嵌入图片附件")
	bfDebug := &boolFlag{val: cfg.Debug}
	flag.Var(bfDebug, "debug", "启用调试日志")
	// 也支持再次传入 --config (但不会再解析文件)
	flag.StringVar(&configPath, "config", configPath, "配置文件路径 (YAML)")

	flag.Parse()

	// 6. 将显式 flag 应用覆盖
	if sfHost.set {
		cfg.IMAPHost = sfHost.val
	}
	if ifPort.set {
		cfg.IMAPPort = ifPort.val
	}
	if sfUser.set {
		cfg.Username = sfUser.val
	}
	if sfPass.set {
		cfg.Password = sfPass.val
	}
	if sfMailbox.set {
		cfg.Mailbox = sfMailbox.val
	}
	if bfTLS.set {
		cfg.UseTLS = bfTLS.val
	}
	if bfStartTLS.set {
		cfg.StartTLS = bfStartTLS.val
	}
	if bfSkip.set {
		cfg.InsecureSkipVerify = bfSkip.val
	}
	if dfInterval.set {
		cfg.CheckInterval = dfInterval.val
	}
	if dfDrain.set {
		cfg.DrainTimeout = dfDrain.val
	}
	if sfWebhook.set {
		cfg.WebhookURL = sfWebhook.val
	}
	if sfHeader.set {
		cfg.WebhookHeader = sfHeader.val
	}
	if ifFetch.set {
		cfg.FetchBodySize = ifFetch.val
	}
	if ifRetryMax.set {
		cfg.RetryMax = ifRetryMax.val
	}
	if dfRetryBackoff.set {
		cfg.RetryBaseBackoff = dfRetryBackoff.val
	}
	if sfHTML.set {
		cfg.HTMLToTextMode = sfHTML.val
	}
	if bfRaw.set {
		cfg.IncludeRawHTML = bfRaw.val
	}
	if bfBlocks.set {
		cfg.EnableBlocks = bfBlocks.val
	}
	if bfSkipInline.set {
		cfg.SkipInlineImages = bfSkipInline.val
	}
	if bfDebug.set {
		cfg.Debug = bfDebug.val
	}

	// 7. 校验
	if cfg.IMAPHost == "" || cfg.Username == "" || cfg.Password == "" || cfg.WebhookURL == "" {
		return nil, fmt.Errorf("缺少必需配置: imap-host/username/password/webhook")
	}
	if cfg.UseTLS && cfg.StartTLS {
		return nil, fmt.Errorf("参数冲突: 不能同时启用 tls 与 starttls")
	}
	if cfg.HTMLToTextMode != "simple" && cfg.HTMLToTextMode != "preserve-line" && cfg.HTMLToTextMode != "none" {
		return nil, fmt.Errorf("html2text 取值非法: %s", cfg.HTMLToTextMode)
	}
	return cfg, nil
}

func mergeFile(path string, base *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("配置文件为空")
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return err
	}
	if fc.IMAPHost != nil {
		base.IMAPHost = *fc.IMAPHost
	}
	if fc.IMAPPort != nil {
		base.IMAPPort = *fc.IMAPPort
	}
	if fc.Username != nil {
		base.Username = *fc.Username
	}
	if fc.Password != nil {
		base.Password = *fc.Password
	}
	if fc.Mailbox != nil {
		base.Mailbox = *fc.Mailbox
	}
	if fc.UseTLS != nil {
		base.UseTLS = *fc.UseTLS
	}
	if fc.StartTLS != nil {
		base.StartTLS = *fc.StartTLS
	}
	if fc.InsecureSkipVerify != nil {
		base.InsecureSkipVerify = *fc.InsecureSkipVerify
	}
	if fc.CheckInterval != nil {
		base.CheckInterval = *fc.CheckInterval
	}
	if fc.DrainTimeout != nil {
		base.DrainTimeout = *fc.DrainTimeout
	}
	if fc.WebhookURL != nil {
		base.WebhookURL = *fc.WebhookURL
	}
	if fc.WebhookHeader != nil {
		base.WebhookHeader = *fc.WebhookHeader
	}
	if fc.FetchBodySize != nil {
		base.FetchBodySize = *fc.FetchBodySize
	}
	if fc.RetryMax != nil {
		base.RetryMax = *fc.RetryMax
	}
	if fc.RetryBaseBackoff != nil {
		base.RetryBaseBackoff = *fc.RetryBaseBackoff
	}
	if fc.HTMLToTextMode != nil {
		base.HTMLToTextMode = *fc.HTMLToTextMode
	}
	if fc.Debug != nil {
		base.Debug = *fc.Debug
	}
	if fc.IncludeRawHTML != nil {
		base.IncludeRawHTML = *fc.IncludeRawHTML
	}
	if fc.EnableBlocks != nil {
		base.EnableBlocks = *fc.EnableBlocks
	}
	if fc.SkipInlineImages != nil {
		base.SkipInlineImages = *fc.SkipInlineImages
	}
	return nil
}

func parseBool(v string) bool { return v == "1" || v == "true" || v == "TRUE" || v == "yes" }

// (legacy helper functions removed as unused)
