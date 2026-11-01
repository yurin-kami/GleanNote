/* 笔记写入用例测试：保存、冲突副本、状态校验、参数校验与移动 */
package note

import (
	"context"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* ptr 返回字符串指针，便于构造可选字段 */
func ptr(value string) *string { return &value }

/* TestUpdateNote 验证修订号推进、正文替换与标题保留 */
func TestUpdateNote(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "原标题", "旧内容")

	result := f.save(t, UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Content: ptr("新内容"), Revision: 1,
	})
	if result.ConflictCopyID != "" {
		t.Fatalf("修订号匹配不应产生冲突副本: %q", result.ConflictCopyID)
	}
	if result.Note.Revision != 2 || result.Note.Content != "新内容" ||
		result.Note.Title != "原标题" || result.Note.CharCount != 3 {
		t.Fatalf("保存结果异常: %+v", result.Note)
	}
}

/* TestUpdateNoteCreatesConflictCopy 验证修订号落后时先存副本再覆盖，且不返回 412 */
func TestUpdateNoteCreatesConflictCopy(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "原标题", "旧内容")
	f.save(t, UpdateNoteInput{UserID: "u-1", NoteID: note.ID, Content: ptr("设备A内容"), Revision: 1})

	result := f.save(t, UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Content: ptr("设备B内容"), Revision: 1,
	})
	if result.ConflictCopyID == "" {
		t.Fatal("修订号落后应返回 conflictCopyId")
	}
	if result.Note.Revision != 3 || result.Note.Content != "设备B内容" {
		t.Fatalf("覆盖后字段异常: %+v", result.Note)
	}

	copied, err := f.service.GetNote(context.Background(), "u-1", result.ConflictCopyID)
	if err != nil || !copied.IsConflictCopy || copied.Content != "设备A内容" ||
		copied.Revision != 1 || !strings.Contains(copied.Title, "冲突副本") {
		t.Fatalf("冲突副本字段异常: %+v err=%v", copied, err)
	}
}

/* TestUpdateNoteRejectsInvalidStateOrInput 覆盖已删除、修订号、来源与长度校验 */
func TestUpdateNoteRejectsInvalidStateOrInput(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "标题", "正文")

	_, err := f.service.UpdateNote(context.Background(), UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Revision: 0,
	})
	requireCode(t, err, errs.CodeValidationFailed)

	_, err = f.service.UpdateNote(context.Background(), UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Revision: 1, SaveSource: "AUTOSAVE",
	})
	requireCode(t, err, errs.CodeValidationFailed)

	_, err = f.service.UpdateNote(context.Background(), UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Revision: 1,
		Title: ptr(strings.Repeat("字", valueobject.MaxNoteTitleLength+1)),
	})
	requireCode(t, err, errs.CodeValidationFailed)

	_, err = f.service.UpdateNote(context.Background(), UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Revision: 1,
		Content: ptr(strings.Repeat("a", valueobject.MaxNoteContentLength+1)),
	})
	requireCode(t, err, errs.CodeNoteTooLarge)

	f.delete(t, "u-1", note.ID)
	_, err = f.service.UpdateNote(context.Background(), UpdateNoteInput{
		UserID: "u-1", NoteID: note.ID, Revision: 2, Content: ptr("x"),
	})
	requireCode(t, err, errs.CodeNoteStateInvalid)
}

/* TestMoveNote 覆盖移动后的路径、目标目录校验与已删除笔记不可移动 */
func TestMoveNote(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.seedFolder(t, "u-1", root, "f-1", "数学")
	note := f.create(t, "u-1", root.ID, "标题", "正文")

	moved, err := f.service.MoveNote(context.Background(), MoveNoteInput{
		UserID: "u-1", NoteID: note.ID, FolderID: sub.ID,
	})
	if err != nil || moved.FolderID != sub.ID || moved.FolderPath != "我的笔记 / 数学" {
		t.Fatalf("移动结果异常: %+v err=%v", moved, err)
	}
	if moved.Revision != 1 {
		t.Fatalf("移动不应改变修订号: %d", moved.Revision)
	}

	_, err = f.service.MoveNote(context.Background(), MoveNoteInput{
		UserID: "u-1", NoteID: note.ID, FolderID: "missing",
	})
	requireCode(t, err, errs.CodeResourceNotFound)

	f.delete(t, "u-1", note.ID)
	_, err = f.service.MoveNote(context.Background(), MoveNoteInput{
		UserID: "u-1", NoteID: note.ID, FolderID: sub.ID,
	})
	requireCode(t, err, errs.CodeNoteStateInvalid)
}
