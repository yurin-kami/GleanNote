/*
 * 目录名称值对象：长度、首尾空格与非法字符的校验规则。
 *
 * 职责：把用户输入转为可信名称，并提供大小写无关的比较键与重名后缀。
 * 不负责：同级重名判定（需要仓储）与路径计算（见 domain/service）。
 * 依赖：标准库。
 * 参考：docs/PRD-拾页笔记-GleanNote.md#51-命名与标题、docs/API.md#523-post-v1folders
 */
package valueobject

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* MaxFolderNameLength 是目录名称的字符上限 */
const MaxFolderNameLength = 50

/* forbiddenFolderNameRunes 是与路径或文件名语义冲突的字符集合 */
const forbiddenFolderNameRunes = `/\:*?"<>|`

/* FolderName 是已通过校验的目录名称 */
type FolderName string

/*
 * ParseFolderName 校验原始输入并返回名称。
 * 约束：1–50 个字符、无首尾空格、不含路径分隔符等非法字符与控制字符。
 */
func ParseFolderName(raw string) (FolderName, error) {
	if raw == "" {
		return "", errs.New(errs.CodeValidationFailed, "目录名称不能为空")
	}
	if raw != strings.TrimSpace(raw) {
		return "", errs.New(errs.CodeValidationFailed, "目录名称不能包含首尾空格")
	}
	if utf8.RuneCountInString(raw) > MaxFolderNameLength {
		return "", errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("目录名称最多 %d 个字符", MaxFolderNameLength))
	}
	for _, char := range raw {
		if strings.ContainsRune(forbiddenFolderNameRunes, char) {
			return "", errs.New(errs.CodeValidationFailed, "目录名称不能包含 / \\ : * ? \" < > |")
		}
		if unicode.IsControl(char) {
			return "", errs.New(errs.CodeValidationFailed, "目录名称不能包含控制字符或换行")
		}
	}
	return FolderName(raw), nil
}

/* String 返回名称原文，供实体与传输层直接使用 */
func (n FolderName) String() string { return string(n) }

/* Key 返回大小写无关的比较键，口径与数据库唯一索引 lower(name) 一致 */
func (n FolderName) Key() string { return strings.ToLower(string(n)) }

/*
 * WithDuplicateSuffix 生成「名称 (n)」用于恢复时避开同级重名。
 * attempt 为第几次尝试（从 1 开始对应「 (2)」）；base 超长时按字符截断。
 */
func (n FolderName) WithDuplicateSuffix(attempt int) FolderName {
	if attempt < 1 {
		return n
	}
	suffix := fmt.Sprintf(" (%d)", attempt+1)
	base := []rune(string(n))
	limit := MaxFolderNameLength - utf8.RuneCountInString(suffix)
	if limit < 1 {
		limit = 1
	}
	if len(base) > limit {
		base = base[:limit]
	}
	return FolderName(string(base) + suffix)
}
