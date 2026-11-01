/*
 * 笔记正文值对象：字符上限与计数口径。
 *
 * 职责：校验 Markdown 原文长度并给出统一字符数，供实体落库与列表展示。
 * 不负责：Markdown 语法校验（正文按纯文本保存）与渲染。
 * 依赖：标准库、domain/errs。
 * 参考：docs/API.md#21-基础约定（按 Unicode 字符计）、PRD FR-06
 */
package valueobject

import (
	"fmt"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* MaxNoteContentLength 是正文的字符上限，与 notes.char_count 检查约束一致 */
const MaxNoteContentLength = 200000

/* NoteContent 是已通过校验的 Markdown 正文；空正文合法 */
type NoteContent string

/*
 * ParseNoteContent 校验原始正文并返回值对象。
 * 约束：Unicode 字符数不超过 20 万；超出返回 NOTE_TOO_LARGE（413）。
 */
func ParseNoteContent(raw string) (NoteContent, error) {
	if utf8.RuneCountInString(raw) > MaxNoteContentLength {
		return "", errs.New(errs.CodeNoteTooLarge,
			fmt.Sprintf("正文最多 %d 个字符", MaxNoteContentLength))
	}
	return NoteContent(raw), nil
}

/* String 返回正文原文 */
func (c NoteContent) String() string { return string(c) }

/* CharCount 返回 Unicode 字符数，与 char_count 列口径一致 */
func (c NoteContent) CharCount() int { return utf8.RuneCountInString(string(c)) }
