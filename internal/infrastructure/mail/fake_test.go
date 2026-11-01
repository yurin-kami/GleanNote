package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* failingWriter 模拟输出失败，用于验证投递错误不会被静默吞掉 */
type failingWriter struct{}

/* Write 始终返回错误，触发 Fake 的错误分支 */
func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("输出不可写") }

/* TestSendRedactsEmailAndCode 验证日志只留邮箱掩码与验证码掩码，完整内容仍可从 Last 读取 */
func TestSendRedactsEmailAndCode(t *testing.T) {
	var out bytes.Buffer
	fake := NewFake(&out, false)
	message := port.MailMessage{
		To:      "demo@example.com",
		Subject: "拾页笔记验证码",
		Body:    "您的验证码是 123456，10 分钟内有效。",
	}
	if err := fake.Send(context.Background(), message); err != nil {
		t.Fatalf("投递邮件: %v", err)
	}

	logged := strings.TrimSpace(out.String())
	if strings.Contains(logged, message.To) || strings.Contains(logged, "123456") {
		t.Fatalf("日志未脱敏: %q", logged)
	}

	/* 单行 JSON 是脚本采集的契约，字段值按脱敏结果输出 */
	var record map[string]string
	if err := json.Unmarshal([]byte(logged), &record); err != nil {
		t.Fatalf("日志不是单行 JSON: %q", logged)
	}
	if record["event"] != "mail.fake.send" {
		t.Fatalf("event = %q，期望 mail.fake.send", record["event"])
	}
	if record["to"] != "d***@example.com" {
		t.Fatalf("to = %q，期望 d***@example.com", record["to"])
	}
	if !strings.HasSuffix(record["body"], "10 分钟内有效。") {
		t.Fatalf("body = %q，非验证码部分应原样保留", record["body"])
	}

	stored, ok := fake.Last()
	if !ok {
		t.Fatal("Last 应返回最近一次投递")
	}
	if stored.To != message.To || stored.Body != message.Body {
		t.Fatalf("内存记录被脱敏改写: %+v", stored)
	}
}

/* TestSendRevealCode 验证仅显式开启时才输出明文，供本地手工联调读取验证码 */
func TestSendRevealCode(t *testing.T) {
	var out bytes.Buffer
	fake := NewFake(&out, true)
	message := port.MailMessage{To: "demo@example.com", Subject: "拾页笔记验证码", Body: "您的验证码是 123456"}
	if err := fake.Send(context.Background(), message); err != nil {
		t.Fatalf("投递邮件: %v", err)
	}
	if !strings.Contains(out.String(), message.To) || !strings.Contains(out.String(), "123456") {
		t.Fatalf("开启 revealCode 后应输出明文: %q", out.String())
	}
}

/* TestSendMaskSingleCharLocal 验证单字符本地部分只保留首字符，非 ASCII 不截出非法 UTF-8 */
func TestSendMaskSingleCharLocal(t *testing.T) {
	if got := maskEmail("a@example.com"); got != "a***@example.com" {
		t.Fatalf("maskEmail = %q", got)
	}
	if got := maskEmail("no-at-sign"); got != "***" {
		t.Fatalf("非法地址 maskEmail = %q，期望 ***", got)
	}
	/* 中文邮箱本地部分按 rune 截取，日志中不得出现替换字符（G-10） */
	if got := maskEmail("张@example.com"); got != "张***@example.com" || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("中文邮箱 maskEmail = %q", got)
	}
}

/* TestSendPropagatesWriteError 验证输出失败向上传递，避免验证码「看似已发送」 */
func TestSendPropagatesWriteError(t *testing.T) {
	fake := NewFake(failingWriter{}, false)
	err := fake.Send(context.Background(), port.MailMessage{To: "demo@example.com", Body: "验证码 123456"})
	if err == nil {
		t.Fatal("写入失败应返回错误")
	}
	if !strings.Contains(err.Error(), "输出邮件记录") {
		t.Fatalf("错误未标注环节: %v", err)
	}
	if _, ok := fake.Last(); ok {
		t.Fatal("写入失败不应记录为已投递")
	}
}

/* TestNewRejectsUnknownProvider 验证未实现的供应商必须失败，禁止静默回落 fake */
func TestNewRejectsUnknownProvider(t *testing.T) {
	if _, err := New("smtp", nil, false); !errors.Is(err, port.ErrProviderNotConfigured) {
		t.Fatalf("New(smtp) 错误 = %v，期望 ErrProviderNotConfigured", err)
	}
	if _, err := New("fake", nil, false); err != nil {
		t.Fatalf("New(fake) 不应失败: %v", err)
	}
}

/* TestHealthFollowsContext 验证无外部依赖时仅由 context 决定健康状态 */
func TestHealthFollowsContext(t *testing.T) {
	fake := NewFake(nil, false)
	if err := fake.Health(context.Background()); err != nil {
		t.Fatalf("上下文正常时 Health = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.Health(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("上下文取消后 Health = %v，期望 context.Canceled", err)
	}
}
