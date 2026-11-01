/* 目录与笔记联动测试：noteCount 回填、移动计数与删除/恢复按批次联动 */
package folder

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* seedNote 直接写入笔记行，绕开笔记用例以便单独验证目录侧联动 */
func (f *fixture) seedNote(t *testing.T, id, userID, folderID, title, content string) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	parsedContent, err := valueobject.ParseNoteContent(content)
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	now := f.clock.Now()
	note := entity.NewNote(id, userID, folderID, parsedTitle, parsedContent, now, now)
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* 列表与详情都应回填未删除笔记数，回收站笔记不计入 */
func TestFolderNoteCount(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.create(t, "u-1", root.ID, "数学")
	f.seedNote(t, "n-1", "u-1", root.ID, "甲", "正文")
	f.seedNote(t, "n-2", "u-1", root.ID, "乙", "正文")
	f.seedNote(t, "n-3", "u-1", sub.ID, "丙", "正文")

	trashed := f.seedNote(t, "n-4", "u-1", root.ID, "已删", "正文")
	trashed.MarkDeleted("batch-old", f.clock.Now(), time.Hour)
	if err := f.notes.Update(context.Background(), trashed); err != nil {
		t.Fatalf("写入回收站笔记: %v", err)
	}

	list := f.listQuery(t, ListFoldersInput{UserID: "u-1", ParentID: root.ID})
	if len(list.Items) != 1 || list.Items[0].NoteCount != 1 {
		t.Fatalf("子目录 noteCount 不符: %+v", list.Items)
	}
	if view := f.currentView(t, "u-1", root.ID); view.NoteCount != 2 {
		t.Fatalf("根目录 noteCount = %d，期望 2", view.NoteCount)
	}
}

/* 移动目录时统计子树内未删除笔记，供 movedNoteCount 回填 */
func TestFolderMoveCountsNotes(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	moving := f.create(t, "u-1", root.ID, "待移动")
	child := f.create(t, "u-1", moving.ID, "子目录")
	target := f.create(t, "u-1", root.ID, "目标")
	f.seedNote(t, "n-1", "u-1", moving.ID, "甲", "正文")
	f.seedNote(t, "n-2", "u-1", child.ID, "乙", "正文")

	result, err := f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: moving.ID, TargetParentID: target.ID,
	})
	if err != nil {
		t.Fatalf("移动失败: %v", err)
	}
	if result.MovedFolderCount != 2 || result.MovedNoteCount != 2 {
		t.Fatalf("移动计数不符: %+v", result)
	}
}

/* 删除目录把子树内笔记并入同一批次，恢复目录时按批次恢复笔记 */
func TestFolderDeleteRestoresNotesByBatch(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.create(t, "u-1", root.ID, "数学")
	f.seedNote(t, "n-1", "u-1", root.ID, "甲", "正文")
	f.seedNote(t, "n-2", "u-1", sub.ID, "乙", "正文")

	deleted, err := f.service.DeleteFolder(context.Background(), "u-1", root.ID)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if deleted.NoteCount != 2 {
		t.Fatalf("删除摘要 noteCount = %d，期望 2", deleted.NoteCount)
	}
	for _, id := range []string{"n-1", "n-2"} {
		note, err := f.notes.GetByID(context.Background(), "u-1", id)
		if err != nil {
			t.Fatalf("读取笔记 %s 失败: %v", id, err)
		}
		if !note.IsDeleted() || note.DeleteBatchID != deleted.DeleteBatchID {
			t.Fatalf("笔记 %s 应并入目录删除批次: %+v", id, note)
		}
	}

	if _, err := f.service.RestoreFolder(context.Background(), "u-1", root.ID); err != nil {
		t.Fatalf("恢复目录失败: %v", err)
	}
	for _, id := range []string{"n-1", "n-2"} {
		note, err := f.notes.GetByID(context.Background(), "u-1", id)
		if err != nil {
			t.Fatalf("读取恢复后笔记 %s 失败: %v", id, err)
		}
		if note.IsDeleted() {
			t.Fatalf("笔记 %s 应随目录恢复: %+v", id, note)
		}
	}
}
