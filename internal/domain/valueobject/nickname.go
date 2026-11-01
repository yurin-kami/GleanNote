/* 昵称值对象：归一化空白、长度与字符集校验，并提供邮箱派生的缺省值 */
package valueobject

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 昵称长度上限，口径见 docs/API.md#512-注册 */
const MaxNicknameLength = 20

/*
 * NormalizeNickname 去除首尾空白并校验昵称。
 * 约束：1–20 个字符；禁止换行与控制字符，避免污染列表展示。
 */
func NormalizeNickname(raw string) (string, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", errs.New(errs.CodeValidationFailed, "昵称不能为空")
	}
	if utf8.RuneCountInString(candidate) > MaxNicknameLength {
		return "", errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("昵称最多 %d 个字符", MaxNicknameLength))
	}
	for _, char := range candidate {
		if unicode.IsControl(char) {
			return "", errs.New(errs.CodeValidationFailed, "昵称不能包含控制字符或换行")
		}
	}
	return candidate, nil
}

/*
 * DefaultNickname 用邮箱 @ 前部分作为缺省昵称。
 * 超长时按字符截断，空结果回落到「用户」，保证注册流程不因昵称失败。
 */
func DefaultNickname(email Email) string {
	candidate := strings.TrimSpace(email.LocalPart())
	if candidate == "" {
		return "用户"
	}
	runes := []rune(candidate)
	if len(runes) > MaxNicknameLength {
		runes = runes[:MaxNicknameLength]
	}
	return string(runes)
}
