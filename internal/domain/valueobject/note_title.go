/*
 * 笔记标题值对象：长度、控制字符与冲突副本后缀规则。
 *
 * 职责：把用户输入转为可信标题，并提供冲突副本标题生成。
 * 不负责：标题去重（同一目录允许重名）与展示兜底文案（由视图映射负责）。
 * 依赖：标准库、domain/errs。
 * 参考：docs/API.md#533-post-v1notes、docs/PRD-拾页笔记-GleanNote.md#51-命名与标题
 */
package valueobject

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* MaxNoteTitleLength 是笔记标题的字符上限 */
const MaxNoteTitleLength = 100

/* UnnamedNoteTitle 是空标题的展示兜底文案，口径见 PRD FR-06 */
const UnnamedNoteTitle = "未命名笔记"

/* conflictSuffixLayout 是冲突副本标题使用的时间格式，按 UTC 展示 */
const conflictSuffixLayout = "2006-01-02 15:04"

/* NoteTitle 是已通过校验的笔记标题；空值合法，展示时由调用方兜底 */
type NoteTitle string

/*
 * ParseNoteTitle 校验原始输入并返回标题。
 * 约束：0–100 个字符、不含控制字符；空串合法（PRD 视为「未命名笔记」）。
 */
func ParseNoteTitle(raw string) (NoteTitle, error) {
	if utf8.RuneCountInString(raw) > MaxNoteTitleLength {
		return "", errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("笔记标题最多 %d 个字符", MaxNoteTitleLength))
	}
	for _, char := range raw {
		if unicode.IsControl(char) {
			return "", errs.New(errs.CodeValidationFailed, "笔记标题不能包含控制字符或换行")
		}
	}
	return NoteTitle(raw), nil
}

/* String 返回标题原文，供实体与传输层直接使用 */
func (t NoteTitle) String() string { return string(t) }

/* Display 返回展示用标题，空标题回落到「未命名笔记」 */
func (t NoteTitle) Display() string {
	if strings.TrimSpace(string(t)) == "" {
		return UnnamedNoteTitle
	}
	return string(t)
}

/*
 * WithConflictSuffix 生成「原标题（冲突副本 时间）」。
 * 约束：总长度不超过上限，超出时按字符截断原标题部分。
 * 参考：docs/API.md#5-业务语义说明、PRD FR-10 同步冲突裁决
 */
func (t NoteTitle) WithConflictSuffix(now time.Time) NoteTitle {
	suffix := fmt.Sprintf("（冲突副本 %s）", now.UTC().Format(conflictSuffixLayout))
	base := []rune(string(t))
	limit := MaxNoteTitleLength - utf8.RuneCountInString(suffix)
	if limit < 1 {
		limit = 1
	}
	if len(base) > limit {
		base = base[:limit]
	}
	return NoteTitle(string(base) + suffix)
}
