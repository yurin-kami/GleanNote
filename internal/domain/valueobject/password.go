/* 密码策略校验：只校验明文强度，不保存明文，哈希由端口完成 */
package valueobject

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * 密码策略常量，口径见 PRD FR-01 与 docs/API.md#512-注册。
 * 长度按 Unicode 字符计，中文、英文、符号各计 1。
 */
const (
	MinPasswordLength = 8
	MaxPasswordLength = 32
)

/*
 * CheckPasswordPolicy 校验明文密码是否满足策略。
 * 约束：8–32 个字符；至少一个字母与一个数字；禁止控制字符与换行。
 * 返回错误必为 VALIDATION_FAILED，细节可直接展示。
 */
func CheckPasswordPolicy(plain string) error {
	length := utf8.RuneCountInString(plain)
	if length < MinPasswordLength || length > MaxPasswordLength {
		return errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("密码长度必须为 %d–%d 个字符", MinPasswordLength, MaxPasswordLength))
	}

	var hasLetter, hasDigit bool
	for _, char := range plain {
		if unicode.IsControl(char) {
			return errs.New(errs.CodeValidationFailed, "密码不能包含控制字符或换行")
		}
		if unicode.IsLetter(char) {
			hasLetter = true
		}
		if unicode.IsDigit(char) {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errs.New(errs.CodeValidationFailed, "密码必须同时包含字母与数字")
	}
	return nil
}
