/* 邮箱验证码实体：负责一次性消费与错误次数作废规则 */
package entity

import (
	"crypto/subtle"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * 验证码用途常量，取值与 docs/API.md#32-枚举字典 一致。
 * 用途参与哈希计算，防止注册验证码被拿去重置密码。
 */
const (
	PurposeRegister      = "REGISTER"
	PurposeResetPassword = "RESET_PASSWORD"
	PurposeDeleteAccount = "DELETE_ACCOUNT"
)

/* MaxCodeAttempts 是同一条验证码允许的连续错误次数 */
const MaxCodeAttempts = 5

/* ValidCodePurpose 判断用途是否为契约枚举值 */
func ValidCodePurpose(purpose string) bool {
	switch purpose {
	case PurposeRegister, PurposeResetPassword, PurposeDeleteAccount:
		return true
	default:
		return false
	}
}

/*
 * EmailCode 是一次验证码发送记录。
 * CodeHash 为 HMAC 摘要，实体不保存 6 位明文验证码。
 */
type EmailCode struct {
	ID         string
	Email      string
	Purpose    string
	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

/*
 * Check 是只读校验：不消费验证码，也不累加错误次数。
 * 用途：在 Argon2id 等昂贵计算前拒绝非法输入，避免未认证请求放大 CPU 消耗。
 */
func (c *EmailCode) Check(providedHash string, now time.Time) error {
	switch {
	case c.ConsumedAt != nil:
		return errs.New(errs.CodeAuthCodeExpired, "验证码已被使用")
	case !now.Before(c.ExpiresAt):
		return errs.New(errs.CodeAuthCodeExpired, "验证码已过期")
	case c.Attempts >= MaxCodeAttempts:
		return errs.New(errs.CodeAuthCodeExpired, "验证码错误次数过多")
	}

	/* 定长摘要比较必须常量时间，避免通过响应时间推测验证码 */
	if subtle.ConstantTimeCompare([]byte(c.CodeHash), []byte(providedHash)) != 1 {
		return errs.New(errs.CodeAuthCodeInvalid, "验证码不正确")
	}
	return nil
}

/*
 * Consume 校验并消费验证码。
 * 顺序：已消费/已过期/次数耗尽 → 错误；哈希不符 → 计一次失败，第 5 次直接作废。
 * 成功后写入 ConsumedAt，调用方必须把结果持久化。
 */
func (c *EmailCode) Consume(providedHash string, now time.Time) error {
	checkErr := c.Check(providedHash, now)
	if checkErr == nil {
		consumed := now
		c.ConsumedAt = &consumed
		return nil
	}
	if errs.IsCode(checkErr, errs.CodeAuthCodeInvalid) {
		return c.fail(now)
	}
	return checkErr
}

/* fail 累加错误次数；达到上限时同时写入消费时间，使后续请求统一返回过期 */
func (c *EmailCode) fail(now time.Time) error {
	c.Attempts++
	if c.Attempts >= MaxCodeAttempts {
		consumed := now
		c.ConsumedAt = &consumed
	}
	return errs.New(errs.CodeAuthCodeInvalid, "验证码不正确").
		WithMeta("attemptsRemaining", MaxCodeAttempts-c.Attempts)
}

/* Expired 表示验证码已过期或已作废，发送新码前应先失效旧的未消费记录 */
func (c *EmailCode) Expired(now time.Time) bool {
	return c.ConsumedAt != nil || !now.Before(c.ExpiresAt)
}
