/*
 * Package mail 提供邮件投递能力的适配器与工厂。
 *
 * 付费邮件服务在本阶段不接入：New 只接受 fake，其余名称返回配置错误。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/gleannote/gleannote/internal/application/port"
)

/*
 * Fake 是开发态邮件投递：把每次投递编码为单行 JSON 写入 writer。
 * 默认脱敏输出；开启 revealCode 时才写出完整收件人与正文，仅供本地手工联调（G-10）。
 * 完整内容始终保留在内存，测试通过 Last 读取。
 */
type Fake struct {
	mu         sync.Mutex
	out        io.Writer
	revealCode bool
	last       port.MailMessage
	hasLast    bool
}

/* fakeRecord 固定输出字段顺序，便于人工核对与被脚本采集；收件人与正文默认脱敏 */
type fakeRecord struct {
	Level   string `json:"level"`
	Event   string `json:"event"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

/* codePattern 匹配 6 位验证码，用于日志脱敏 */
var codePattern = regexp.MustCompile(`\d{6}`)

/* maskEmail 只保留本地部分首字符与域名，避免日志落完整邮箱 */
func maskEmail(address string) string {
	trimmed := strings.TrimSpace(address)
	at := strings.LastIndex(trimmed, "@")
	if at <= 0 {
		return "***"
	}
	/* 按 rune 取首字符，非 ASCII 本地部分（如中文邮箱）不会截出非法 UTF-8（G-10） */
	return string([]rune(trimmed)[0]) + "***" + trimmed[at:]
}

/* redactBody 把正文中的验证码替换为掩码，防止日志泄露可复用凭据 */
func redactBody(body string) string {
	return codePattern.ReplaceAllString(body, "***")
}

/* NewFake 创建开发态实现；out 为 nil 时只保留内存记录，revealCode 仅用于本地手工联调 */
func NewFake(out io.Writer, revealCode bool) *Fake {
	if out == nil {
		out = io.Discard
	}
	return &Fake{out: out, revealCode: revealCode}
}

/*
 * New 按名称创建邮件提供者。
 * 真实供应商未实现时必须失败，禁止回落到 fake 掩盖配置错误。
 */
func New(name string, out io.Writer, revealCode bool) (port.Mailer, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "fake":
		return NewFake(out, revealCode), nil
	default:
		return nil, fmt.Errorf("%w: mailer provider %q", port.ErrProviderNotConfigured, name)
	}
}

/* Name 返回提供者名称，用于启动日志与就绪探针展示 */
func (f *Fake) Name() string { return "fake" }

/*
 * Send 记录邮件并输出一行 JSON。
 * 默认对收件人与验证码脱敏；仅显式开启 revealCode 时输出明文，便于本地手工联调。
 * 输出失败必须向上传递，避免验证码「看似已发送」。
 */
func (f *Fake) Send(ctx context.Context, msg port.MailMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	to, body := maskEmail(msg.To), redactBody(msg.Body)
	if f.revealCode {
		to, body = msg.To, msg.Body
	}
	payload, err := json.Marshal(fakeRecord{
		Level:   "info",
		Event:   "mail.fake.send",
		To:      to,
		Subject: msg.Subject,
		Body:    body,
	})
	if err != nil {
		return fmt.Errorf("编码邮件记录: %w", err)
	}

	/* 写入与状态记录放在同一临界区，避免并发投递时输出交错 */
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := fmt.Fprintf(f.out, "%s\n", payload); err != nil {
		return fmt.Errorf("输出邮件记录: %w", err)
	}
	f.last = msg
	f.hasLast = true
	return nil
}

/*
 * Last 返回最近一次投递内容，供集成测试断言验证码文案。
 * 第二个返回值为 false 表示进程内尚未投递过邮件。
 */
func (f *Fake) Last() (port.MailMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, f.hasLast
}

/* Health 无外部依赖，仅在 context 已取消时返回错误 */
func (f *Fake) Health(ctx context.Context) error {
	return ctx.Err()
}
