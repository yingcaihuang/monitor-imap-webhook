# monitor-imap-webhook

一个用 Go 实现的 IMAP 邮件实时监控工具：通过 IDLE (fallback 轮询) 监听新邮件，解析主题 / 发件人 / 正文（支持 HTML -> 文本转换与多字符集），并以 JSON Webhook 推送。

## 特性

* 实时：优先使用 IMAP IDLE，自动 NOOP 保活；失效时回退重连与轮询
* 稳定：指数回退重连，掉线自动恢复
* 解析：支持多部件 multipart/alternative，优先纯文本；若仅有 HTML 自动剥离标签
* 可选原始：可输出 `raw_html` 原文与基础结构化 `blocks`（heading / paragraph / list / blockquote / code）
* 附件检测：输出 `has_attachments` / `attachment_count` 与 `attachments` 文件名列表（基于 BodyStructure，支持 RFC2047 解码、去重；可选跳过内联图片）
* 编码：自动解码 RFC2047 编码主题，支持常见中文编码（GB2312/GBK -> UTF-8）
* 安全：支持 TLS / STARTTLS，可选跳过证书验证（测试环境）
* Webhook：JSON POST，失败重试（指数退避），可自定义附加 HTTP Header
* 性能：按需抓取，事件驱动；缓冲通道防止阻塞

## 快速开始

```bash
# 克隆并进入
# git clone <your-repo-url>
cd monitor-imap-webhook

# 运行（示例：SSL 方式 993 端口）
IMAP_HOST=imap.example.com \
IMAP_PORT=993 \
IMAP_USERNAME=user@example.com \
IMAP_PASSWORD='app-password' \
WEBHOOK_URL='http://127.0.0.1:8080/mail' \
go run ./cmd/monitor --mailbox INBOX --tls --html2text simple
```

### 使用配置文件 (YAML)

支持通过 `--config` 指定 YAML 文件，示例参见 `config.example.yaml`。

优先级 (高 -> 低)：

1. 命令行参数
2. 配置文件 (`--config`)
3. 环境变量
4. 内部默认值

示例：

```bash
go build -o monitor ./cmd/monitor
./monitor --config ./config.example.yaml --mailbox INBOX
```

示例 YAML 字段（部分）：

```yaml
imap_host: imap.example.com
imap_port: 993
username: user@example.com
password: example-pass
mailbox: INBOX
tls: true          # 与 starttls 互斥
starttls: false
insecure_skip_verify: false
interval: 30s
webhook: http://127.0.0.1:8080/mail
webhook_header: X-Token=abc123;X-Env=dev
fetch_body_bytes: 204800
retry_max: 5
retry_backoff: 1s
html2text: simple   # simple|preserve-line|none
```

> YAML 中任意字段可被同名命令行参数再次覆盖；未出现在 YAML 的字段可继续由环境变量提供。

### 主要命令行参数 / 环境变量

| 参数 | 环境变量 | 说明 | 默认 |
|------|----------|------|------|
| --imap-host | IMAP_HOST | IMAP 服务器主机 | (必填) |
| --imap-port | IMAP_PORT | IMAP 端口 | 993 |
| --username | IMAP_USERNAME | 用户名 | (必填) |
| --password | IMAP_PASSWORD | 密码/应用专用密码 | (必填) |
| --mailbox | IMAP_MAILBOX | 监听的邮箱文件夹 | INBOX |
| --tls | IMAP_TLS | 直接 TLS 连接 | true |
| --starttls | IMAP_STARTTLS | 普通连接后升级 STARTTLS | false |
| --insecure-skip-verify | IMAP_INSECURE_SKIP_VERIFY | 跳过证书验证(测试) | false |
| --interval | IMAP_INTERVAL | 无 IDLE 时轮询间隔 | 30s |
| --webhook | WEBHOOK_URL | Webhook 接收地址 | (必填) |
| --webhook-header | WEBHOOK_HEADER | 附加 Header `K=V;K2=V2` | (空) |
| --fetch-body-bytes | FETCH_BODY_BYTES | 抓取正文最大字节 | 204800 |
| --retry-max | RETRY_MAX | Webhook 最大重试次数 | 5 |
| --retry-backoff | RETRY_BACKOFF | 初始退避时长 | 1s |
| --html2text | HTML2TEXT_MODE | HTML 转文本策略 (simple / preserve-line / none) | simple |
| --raw-html | RAW_HTML | 在 payload 中包含原始 HTML | false |
| --enable-blocks | ENABLE_BLOCKS | 基于 HTML 构建轻量 blocks AST | false |
| --skip-inline-images | SKIP_INLINE_IMAGES | 忽略 disposition=inline 且为 image/* 的内联图片附件 | false |
| --debug | DEBUG | 启用调试日志 | false |

> 优先级：命令行 > 环境变量 > 内部默认值。

### HTML 转文本策略

* simple: 折叠空白，去标签
* preserve-line: 保留段落/换行（换行与段落标签转换为换行）
* none: 不处理，原样保留 HTML

### Webhook Payload 示例

```json
{
  "uid": 123,
  "subject": "测试主题",
  "from": "Alice <alice@example.com>",
  "date": "Mon, 28 Sep 2025 12:00:00 +0800",
  "body": "这是纯文本内容 Plain",
  "preview": "这是纯文本内容 Plain",
  "body_lines": ["这是纯文本内容 Plain"],
  "word_count": 2,
  "has_attachments": true,
  "attachment_count": 2,
  "attachments": ["agenda.pdf", "说明.docx"],
  "mailbox": "INBOX",
  "timestamp": 1727500000
}
```

启用 `--raw-html --enable-blocks` 后 (示意)：

```json
{
  "uid": 123,
  "subject": "测试主题",
  "raw_html": "<html><body><p>这是纯文本内容 <b>Plain</b></p></body></html>",
  "blocks": [
    {"type":"paragraph","text":"这是纯文本内容 Plain"}
  ]
}
```

### 重试与回退

* IMAP 连接失败：指数回退 1s,2s,4s... 上限 ~30s
* Webhook 发送失败：基础 backoff = `--retry-backoff`，每次 *2，最多 `--retry-max` 次

### 日志示例

```text
启动: host=imap.example.com port=993 mailbox=INBOX webhook=http://127.0.0.1:8080/mail
imapclient 2025/09/28 10:00:00.123456 connected imap.example.com:993
imapclient 2025/09/28 10:00:00.456789 login ok
Webhook 已发送 UID=123 主题=测试主题...

```

### 调试模式 (Debug)

启用 `--debug` 或环境变量 `DEBUG=1` 可输出更细粒度的 IMAP 状态转换、IDLE 生命周期与抓取范围：

```text
imapclient 2025/09/28 11:00:00.000001 dial ok imap.example.com:993
imapclient 2025/09/28 11:00:00.050321 login ok user=user@example.com
imapclient 2025/09/28 11:00:00.051234 mailbox selected messages=42
imapclient 2025/09/28 11:00:00.051900 enter IDLE baseline=42
imapclient 2025/09/28 11:05:10.123456 new messages detected count=1 triggering fetch
imapclient 2025/09/28 11:05:10.125678 fetch range 43:43
imapclient 2025/09/28 11:05:10.125999 emit uid=1234
imapclient 2025/09/28 11:05:10.126500 update baseline=43 restart idle
```

便于定位：

* 提前被服务器断开（关注 idle finished err）
* 新邮件到达但未触发（确认 MailboxUpdate 与 fetch range）
* UID 漏发（查看 emit uid 顺序）

```text
```

### 调试：short write / IDLE 丢事件 / 并发冲突

常见问题 & 解决：

* short write / unexpected EOF / connection reset

* 原因：同时向 IMAP 服务器发送了并发命令（IDLE + FETCH），某些服务器在退出 IDLE 前发送其他命令会导致写入截断。
* 解决：所有底层命令通过 `Exec()` 串行化互斥执行；IDLE 退出与重新进入之间不会交叉其他命令。

* 新邮件未触发 (服务器没推送或事件丢失)

* 处理：周期性 ticker 强制结束当前 IDLE，调用 STATUS/SELECT 获取最新消息数，再计算差异补发。

* IDLE 与正文抓取竞争导致连接被服务端关闭

* 处理：实现 drain 机制：当发现新 UID 后，`BeginProcess()` 增加活跃计数；正文抓取与 webhook 完成后调用 `EndProcess()`。IDLE 循环在重新进入前调用 `drain()` 等待 `activeFetches==0` 或超时 (由 `--drain-timeout` 控制)。

* 乱码 / 编码问题

* 标题：使用 `mime.WordDecoder` 解码 RFC2047；正文：依赖 go-message/charset 做转换；无法识别时保留原文。

* HTML 变成一行 / 换行丢失

* 使用 `--html2text preserve-line` 保留换行。

### blocks 结构说明 (实验特性)

启用 `--enable-blocks` 后，会尝试从 HTML 中提取基础语义块：

| type | 字段 | 说明 |
|------|------|------|
| heading | level,text | h1-h6 标题 |
| paragraph | text | 段落文本 |
| list | ordered,items | 有序/无序列表 |
| blockquote | text | 引用块 |
| code | text | 代码块 (pre/code 区块) |

### 附件字段

判定规则：遍历 BodyStructure 叶子 part：

1. 若其 `Disposition` 为 `attachment` 或 `inline`
2. 且存在文件名参数：`name` / `filename` / disposition 参数中的同名键
3. 则视为一个附件条目；文件名会进行 RFC2047 / charset 解码。

特性：

* `has_attachments`: 是否存在任意附件
* `attachment_count`: 附件数量（去重后）
* `attachments`: 去重（保持首次出现顺序）的文件名列表
* `--skip-inline-images`：若开启并且附件为 `Disposition=inline` 且 MIME 主类型为 `image`（例如签名里嵌入的小图标 / logo），则忽略，不计入上述统计
* 文件名会尝试 RFC2047 解码，无法解码保留原文
* 仅列出名称，不抓取内容；后续可拓展大小、MIME、哈希等

### 预览与词数逻辑

* 预览优先使用首个“语义行”——过滤掉疑似样式/模板噪音行（包含 `{` / `@media` / `font-family` 等）
* 若正文为空但存在附件，则预览回退为 `Attachments: name1, name2 (+N more)` 格式（最多列出 5 个文件名）
* 词数统计：
  * 英文/数字：按空白与连续字母数字序列为一个词
  * CJK（中日韩统一表意区 0x4E00–0x9FFF）：按单字计数
  * 其它字符/标点作为分隔符忽略

实现是启发式正则，不保证对复杂/嵌套 HTML 完全准确；复杂需求建议后续接入真正的 HTML 解析库。

### 性能 / 截断策略

* `fetch_body_bytes` 控制抓取上限，超出截断附加标记 `...<truncated>`。
* blocks 构建只在启用并存在 HTML 时执行，纯文本邮件无额外开销。

## 构建

```bash
go build -o bin/monitor ./cmd/monitor
```

## 运行测试

```bash
go test ./...
```

## 设计要点

* 事件流：IMAP IDLE -> MailboxUpdate -> recent UID 计算 -> 解析 -> Webhook
* 解析策略：优先 text/plain；无则 HTML -> 文本
* 线程安全：底层 *client.Client 使用互斥锁访问 (Raw 方法只读)
* 截断保护：正文超过配置字节数截断，防止超大邮件导致内存压力

## 可扩展建议 (Future Work)

* HMAC 签名（对 body 计算 HMAC-SHA256 添加 Header）
* 去重缓存（防止某些服务器重复推送）
* 并发抓取（worker pool）
* Prometheus 指标 / pprof 暴露
* 邮件附件解析与过滤

## 风险与注意

* `--insecure-skip-verify` 仅用于测试环境
* 某些旧服务器不完全支持 IDLE，将自动 fallback 轮询
* HTML 转文本当前为简单实现，若需精确排版可接入成熟库（如 bluemonday + goquery）

## License

自定义/内部项目（如需开源可改为 MIT / Apache-2.0）
