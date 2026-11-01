/* 笔记实体测试：构造、状态机、回收站与冲突副本 */
package entity

import (
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* newTestNote 构造一份固定时钟下的笔记，减少各行重复 */
func newTestNote(t *testing.T) *Note {
	t.Helper()
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	note := NewNote("note-1", "user-1", "folder-1",
		valueobject.NoteTitle("矩阵的秩"), valueobject.NoteContent("正文"), now, now)
	return note
}

/* TestNewNote 校验初始状态、修订号与字符数 */
func TestNewNote(t *testing.T) {
	note := newTestNote(t)
	if note.State != NoteStateNormal || note.Revision != 1 || note.CharCount != 2 {
		t.Fatalf("初始字段不符合契约: %+v", note)
	}
	if note.ConflictCopy {
		t.Fatal("普通笔记不应标记为冲突副本")
	}
}

/* TestNoteSave 验证保存推进修订号、空标题兜底与已删除拒编辑 */
func TestNoteSave(t *testing.T) {
	note := newTestNote(t)
	now := note.UpdatedAt.Add(time.Minute)
	if err := note.Save(valueobject.NoteTitle(""), valueobject.NoteContent("新正文"), now); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if note.Revision != 2 || note.CharCount != 3 || note.DisplayTitle() != valueobject.UnnamedNoteTitle {
		t.Fatalf("保存后字段异常: %+v", note)
	}

	note.MarkDeleted("batch-1", now, 30*24*time.Hour)
	if err := note.Save(valueobject.NoteTitle("x"), valueobject.NoteContent(""), now); !errs.IsCode(err, errs.CodeNoteStateInvalid) {
		t.Fatalf("已删除笔记保存应返回 NOTE_STATE_INVALID，实际 %v", err)
	}
}

/* TestNoteDraftBecomesNormal 验证 DRAFT 首次保存自动转为 NORMAL */
func TestNoteDraftBecomesNormal(t *testing.T) {
	note := newTestNote(t)
	note.State = NoteStateDraft
	if err := note.Save(valueobject.NoteTitle("草稿"), valueobject.NoteContent("正文"), note.UpdatedAt); err != nil {
		t.Fatalf("草稿保存失败: %v", err)
	}
	if note.State != NoteStateNormal {
		t.Fatalf("state = %s，期望 NORMAL", note.State)
	}
}

/* TestNoteMoveDeleteRestore 覆盖移动、软删除幂等与恢复清空 */
func TestNoteMoveDeleteRestore(t *testing.T) {
	note := newTestNote(t)
	now := note.UpdatedAt.Add(time.Hour)
	if err := note.Move("folder-2", now); err != nil || note.FolderID != "folder-2" {
		t.Fatalf("移动失败: %v folder=%s", err, note.FolderID)
	}

	note.MarkDeleted("batch-1", now, 30*24*time.Hour)
	purgeAt := *note.PurgeAt
	note.MarkDeleted("batch-2", now.Add(time.Hour), 30*24*time.Hour)
	if note.DeleteBatchID != "batch-1" || !note.PurgeAt.Equal(purgeAt) {
		t.Fatalf("重复删除不应改写回收站记录: %+v", note)
	}

	note.Restore("root-1", now)
	if note.IsDeleted() || note.State != NoteStateNormal || note.FolderID != "root-1" || note.DeleteBatchID != "" {
		t.Fatalf("恢复后字段异常: %+v", note)
	}
}

/* TestNewConflictCopy 验证冲突副本独立成篇并带标记 */
func TestNewConflictCopy(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	copied := NewConflictCopy("note-2", "user-1", "folder-1",
		valueobject.NoteTitle("原标题（冲突副本 2026-10-28 06:00）"), valueobject.NoteContent("旧正文"), now)
	if !copied.ConflictCopy || copied.Revision != 1 || copied.Content != "旧正文" {
		t.Fatalf("冲突副本字段异常: %+v", copied)
	}
}
