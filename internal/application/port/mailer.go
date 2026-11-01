/*
 * 邮件投递端口。
 *
 * 邮件服务按量计费，开发与测试阶段必须使用 fake 实现：
 * 验证码只输出到进程标准输出，不产生任何外呼与费用。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */

package port

import "context"

/*
 * MailMessage 是归一后的邮件内容。
 * Body 为纯文本，验证码等敏感信息不得写入结构化日志字段。
 */
type MailMessage struct {
	To      string
	Subject string
	Body    string
}

/*
 * Mailer 是邮件投递能力的端口。
 * Send 返回错误时用例按「验证码发送失败」处理，不得无限重试。
 */
type Mailer interface {
	Name() string
	Send(ctx context.Context, msg MailMessage) error
	Health(ctx context.Context) error
}
