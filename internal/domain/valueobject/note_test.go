/* 笔记标题与正文值对象测试：边界长度、控制字符与冲突副本标题 */
package valueobject

import (
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestParseNoteTitle 覆盖空标题、上限与非法字符三类口径 */
func TestParseNoteTitle(t *testing.T) {
	if title, err := ParseNoteTitle(""); err != nil || title.Display() != UnnamedNoteTitle {
		t.Fatalf("空标题应合法且展示兜底，实际 %q err=%v", title, err)
	}
	if _, err := ParseNoteTitle(strings.Repeat("字", MaxNoteTitleLength)); err != nil {
		t.Fatalf("上限内标题应合法: %v", err)
	}
	_, err := ParseNoteTitle(strings.Repeat("字", MaxNoteTitleLength+1))
	if !errs.IsValidation(err) {
		t.Fatalf("超长标题应返回 VALIDATION_FAILED，实际 %v", err)
	}
	if _, err := ParseNoteTitle("换\n行"); !errs.IsValidation(err) {
		t.Fatalf("控制字符应返回 VALIDATION_FAILED，实际 %v", err)
	}
}

/* TestNoteTitleWithConflictSuffix 验证后缀格式与总长不超过上限 */
func TestNoteTitleWithConflictSuffix(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	got := NoteTitle("矩阵的秩").WithConflictSuffix(now)
	if got.String() != "矩阵的秩（冲突副本 2026-10-28 06:00）" {
		t.Fatalf("冲突副本标题 = %q", got.String())
	}
	long := NoteTitle(strings.Repeat("字", MaxNoteTitleLength)).WithConflictSuffix(now)
	if len([]rune(long.String())) > MaxNoteTitleLength {
		t.Fatalf("冲突副本标题超长: %d", len([]rune(long.String())))
	}
}

/* TestParseNoteContent 覆盖空正文、上限与超限三类口径 */
func TestParseNoteContent(t *testing.T) {
	content, err := ParseNoteContent("")
	if err != nil || content.CharCount() != 0 {
		t.Fatalf("空正文应合法，实际 %q err=%v", content, err)
	}
	atLimit := strings.Repeat("a", MaxNoteContentLength)
	if _, err := ParseNoteContent(atLimit); err != nil {
		t.Fatalf("上限内正文应合法: %v", err)
	}
	_, err = ParseNoteContent(atLimit + "a")
	if !errs.IsCode(err, errs.CodeNoteTooLarge) {
		t.Fatalf("超限正文应返回 NOTE_TOO_LARGE，实际 %v", err)
	}
}
