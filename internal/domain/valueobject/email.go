/* 邮箱值对象：负责归一化（小写、去空白）与格式合法性判定 */
package valueobject

import (
	"net/mail"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* MaxEmailLength 是契约允许的邮箱最大字符数 */
const MaxEmailLength = 254

/* maxLocalPartLength 是 RFC 5321 对 @ 前部分的长度上限 */
const maxLocalPartLength = 64

/*
 * Email 只保存归一化后的地址。
 * 不保存原文，避免同一邮箱因大小写或空白产生多条账号。
 */
type Email struct {
	value string
}

/*
 * ParseEmail 校验并归一化邮箱。
 * 约束：去首尾空白、整体转小写；必须能解析为无显示名的单一地址，域名含点号。
 */
func ParseEmail(raw string) (Email, error) {
	candidate := strings.ToLower(strings.TrimSpace(raw))
	if err := checkEmailShape(candidate); err != nil {
		return Email{}, err
	}
	return Email{value: candidate}, nil
}

/* checkEmailShape 是纯格式校验，返回可直接展示给客户端的校验细节 */
func checkEmailShape(candidate string) error {
	if candidate == "" {
		return errs.New(errs.CodeValidationFailed, "邮箱不能为空")
	}
	if len(candidate) > MaxEmailLength {
		return errs.New(errs.CodeValidationFailed, "邮箱长度超过 254 个字符")
	}
	parsed, err := mail.ParseAddress(candidate)
	if err != nil || parsed.Name != "" || parsed.Address != candidate {
		return errs.New(errs.CodeValidationFailed, "邮箱格式不合法")
	}
	at := strings.LastIndex(candidate, "@")
	if at <= 0 || at == len(candidate)-1 {
		return errs.New(errs.CodeValidationFailed, "邮箱格式不合法")
	}
	local, domain := candidate[:at], candidate[at+1:]
	if len(local) > maxLocalPartLength {
		return errs.New(errs.CodeValidationFailed, "邮箱 @ 前部分超过 64 个字符")
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return errs.New(errs.CodeValidationFailed, "邮箱域名不合法")
	}
	return nil
}

/* String 返回归一化后的邮箱地址 */
func (e Email) String() string {
	return e.value
}

/* LocalPart 返回 @ 前部分，用于生成缺省昵称 */
func (e Email) LocalPart() string {
	at := strings.LastIndex(e.value, "@")
	if at <= 0 {
		return ""
	}
	return e.value[:at]
}

/* Equal 比较两个邮箱是否指向同一账号 */
func (e Email) Equal(other Email) bool {
	return e.value == other.value
}
