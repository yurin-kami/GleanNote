/*
 * 标签名称值对象：长度、首尾空格与控制字符的校验规则。
 *
 * 职责：把用户输入转为可信标签名，并提供大小写无关的比较键。
 * 不负责：账号内重名判定（需要仓储）与关联笔记数量统计。
 * 依赖：标准库、domain/errs。
 * 参考：docs/API.md#57-标签、docs/PRD-拾页笔记-GleanNote.md#51-命名与标题
 */
package valueobject

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* MaxTagNameLength 是标签名称的字符上限 */
const MaxTagNameLength = 20

/* TagName 是已通过校验的标签名称 */
type TagName string

/*
 * ParseTagName 校验原始输入并返回标签名。
 * 约束：1–20 个字符、无首尾空格、不含控制字符或换行。
 */
func ParseTagName(raw string) (TagName, error) {
	if raw == "" {
		return "", errs.New(errs.CodeValidationFailed, "标签名称不能为空")
	}
	if raw != strings.TrimSpace(raw) {
		return "", errs.New(errs.CodeValidationFailed, "标签名称不能包含首尾空格")
	}
	if utf8.RuneCountInString(raw) > MaxTagNameLength {
		return "", errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("标签名称最多 %d 个字符", MaxTagNameLength))
	}
	for _, char := range raw {
		if unicode.IsControl(char) {
			return "", errs.New(errs.CodeValidationFailed, "标签名称不能包含控制字符或换行")
		}
	}
	return TagName(raw), nil
}

/* String 返回名称原文，供实体与传输层直接使用 */
func (n TagName) String() string { return string(n) }

/* Key 返回大小写无关的比较键，账号内唯一性按此口径判定 */
func (n TagName) Key() string { return strings.ToLower(string(n)) }
