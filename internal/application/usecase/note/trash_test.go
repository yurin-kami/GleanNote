/* 笔记回收站用例测试：软删除幂等、原地恢复、目录缺失回落与彻底删除 */
package note

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestDeleteNote 验证回收站摘要与重复删除保持首次批次 */
func TestDeleteNote(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "标题", "正文")

	first := f.delete(t, "u-1", note.ID)
	if first.DeleteBatchID == "" || !first.PurgeAt.Equal(f.clock.Now().Add(NoteTrashRetention)) {
		t.Fatalf("删除结果异常: %+v", first)
	}
	f.clock.Advance(time.Hour)
	again := f.delete(t, "u-1", note.ID)
	if again.DeleteBatchID != first.DeleteBatchID || !again.PurgeAt.Equal(first.PurgeAt) {
		t.Fatalf("重复删除应保持首次批次: %+v", again)
	}

	stored, err := f.notes.GetByID(context.Background(), "u-1", note.ID)
	if err != nil || stored.State != entity.NoteStateDeleted || stored.DeleteBatchID != first.DeleteBatchID {
		t.Fatalf("软删除字段异常: %+v err=%v", stored, err)
	}

	_, err = f.service.GetNote(context.Background(), "u-2", note.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}

/* TestRestoreNoteInPlace 验证原目录可用时原地恢复 */
func TestRestoreNoteInPlace(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.seedFolder(t, "u-1", root, "f-1", "数学")
	note := f.create(t, "u-1", sub.ID, "标题", "正文")
	f.delete(t, "u-1", note.ID)

	result, err := f.service.RestoreNote(context.Background(), "u-1", note.ID)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if result.RestoredToRoot || result.Note.State != string(entity.NoteStateNormal) ||
		result.Note.FolderID != sub.ID || result.Note.Content != "正文" {
		t.Fatalf("恢复结果异常: %+v", result)
	}

	_, err = f.service.RestoreNote(context.Background(), "u-1", note.ID)
	requireCode(t, err, errs.CodePreconditionFailed)
}

/* TestRestoreNoteRestoresDeletedFolder 验证原目录在回收站时先恢复目录 */
func TestRestoreNoteRestoresDeletedFolder(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.seedFolder(t, "u-1", root, "f-1", "数学")
	note := f.create(t, "u-1", sub.ID, "标题", "")
	f.delete(t, "u-1", note.ID)

	/* 目录随批次一起进入回收站，笔记恢复前必须把目录恢复出来 */
	sub.MarkDeleted("folder-batch", f.clock.Now(), NoteTrashRetention)
	if err := f.folders.Update(context.Background(), sub); err != nil {
		t.Fatalf("软删除目录失败: %v", err)
	}

	result, err := f.service.RestoreNote(context.Background(), "u-1", note.ID)
	if err != nil || result.Note.FolderID != sub.ID {
		t.Fatalf("目录在回收站时应恢复原目录: %+v err=%v", result, err)
	}
	folder, err := f.folders.GetByID(context.Background(), "u-1", sub.ID)
	if err != nil || folder.IsDeleted() {
		t.Fatalf("目录未被恢复: %+v err=%v", folder, err)
	}
}

/* TestRestoreNoteFallsBackToRoot 验证原目录已彻底删除时回到根级 */
func TestRestoreNoteFallsBackToRoot(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.seedFolder(t, "u-1", root, "f-1", "数学")
	note := f.create(t, "u-1", sub.ID, "标题", "")
	f.delete(t, "u-1", note.ID)

	sub.MarkDeleted("folder-batch", f.clock.Now(), NoteTrashRetention)
	if err := f.folders.Update(context.Background(), sub); err != nil {
		t.Fatalf("软删除目录失败: %v", err)
	}
	if _, err := f.folders.DeleteBatch(context.Background(), "u-1", sub.ID); err != nil {
		t.Fatalf("彻底删除目录失败: %v", err)
	}

	result, err := f.service.RestoreNote(context.Background(), "u-1", note.ID)
	if err != nil || !result.RestoredToRoot || result.Note.FolderID != root.ID {
		t.Fatalf("原目录缺失应回落根级: %+v err=%v", result, err)
	}
}

/* TestPurgeExpiredNotes 验证到期清理只删除 purge_at 已过的笔记，未到期保持 */
func TestPurgeExpiredNotes(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	expired := f.create(t, "u-1", root.ID, "到期", "")
	fresh := f.create(t, "u-1", root.ID, "未到期", "")
	f.delete(t, "u-1", expired.ID)
	f.delete(t, "u-1", fresh.ID)

	/* 只让第一篇到期：把它的 purge_at 提前，第二篇保持原保留期 */
	stored, err := f.notes.GetByID(context.Background(), "u-1", expired.ID)
	if err != nil {
		t.Fatalf("读取到期笔记: %v", err)
	}
	stored.PurgeAt = ptrTime(f.clock.Now().Add(time.Minute))
	if err := f.notes.Update(context.Background(), stored); err != nil {
		t.Fatalf("调整到期时间: %v", err)
	}
	f.clock.Advance(2 * time.Minute)

	purged, err := f.service.PurgeExpiredNotes(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("到期清理结果 = %d, err=%v", purged, err)
	}
	if _, err := f.service.GetNote(context.Background(), "u-1", expired.ID); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("到期笔记应被彻底删除: %v", err)
	}
	if _, err := f.service.GetNote(context.Background(), "u-1", fresh.ID); err != nil {
		t.Fatalf("未到期笔记应保留: %v", err)
	}
}

/* ptrTime 返回时间指针，供构造 purge_at 边界 */
func ptrTime(t time.Time) *time.Time { return &t }

/* TestExpungeNote 验证仅回收站笔记可彻底删除且删除后不可读 */
func TestExpungeNote(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "标题", "")

	err := f.service.ExpungeNote(context.Background(), "u-1", note.ID)
	requireCode(t, err, errs.CodePreconditionFailed)

	f.delete(t, "u-1", note.ID)
	if err := f.service.ExpungeNote(context.Background(), "u-1", note.ID); err != nil {
		t.Fatalf("彻底删除失败: %v", err)
	}
	_, err = f.service.GetNote(context.Background(), "u-1", note.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}
