/* 测试内存笔记仓储：可见性过滤、排序分页、按目录批处理与回滚 */
package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* noteNow 是仓储测试的固定基准时间，便于断言排序键 */
var noteNow = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

/* newNote 构造一篇笔记，正文与标题按值对象校验 */
func newNote(t *testing.T, id, userID, folderID, title, content string, now time.Time) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	parsedContent, err := valueobject.ParseNoteContent(content)
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	return entity.NewNote(id, userID, folderID, parsedTitle, parsedContent, now, now)
}

/* TestNoteRepositoryCreateAndGet 覆盖创建、重复 ID 与跨用户不可见 */
func TestNoteRepositoryCreateAndGet(t *testing.T) {
	repo := NewNoteRepository(NewStore())
	ctx := context.Background()

	if err := repo.Create(ctx, newNote(t, "n-1", "u-1", "f-1", "标题", "正文", noteNow)); err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}
	if err := repo.Create(ctx, newNote(t, "n-1", "u-1", "f-1", "重复", "", noteNow)); err == nil {
		t.Fatal("重复 ID 应失败")
	}
	stored, err := repo.GetByID(ctx, "u-1", "n-1")
	if err != nil || stored.Title != "标题" || stored.CharCount != 2 {
		t.Fatalf("读取笔记异常: %+v err=%v", stored, err)
	}
	if _, err := repo.GetByID(ctx, "u-2", "n-1"); err == nil {
		t.Fatal("跨用户读取应返回 RESOURCE_NOT_FOUND")
	}
}

/* TestNoteRepositoryList 覆盖默认排除回收站、状态过滤、时间过滤与排序分页 */
func TestNoteRepositoryList(t *testing.T) {
	repo := NewNoteRepository(NewStore())
	ctx := context.Background()

	first := newNote(t, "n-1", "u-1", "f-1", "Alpha", "", noteNow)
	second := newNote(t, "n-2", "u-1", "f-1", "beta", "", noteNow.Add(time.Minute))
	third := newNote(t, "n-3", "u-1", "f-2", "Gamma", "", noteNow.Add(2*time.Minute))
	deleted := newNote(t, "n-4", "u-1", "f-1", "已删除", "", noteNow.Add(3*time.Minute))
	deleted.MarkDeleted("batch-1", noteNow.Add(3*time.Minute), 30*24*time.Hour)
	for _, note := range []*entity.Note{first, second, third, deleted} {
		if err := repo.Create(ctx, note); err != nil {
			t.Fatalf("写入笔记失败: %v", err)
		}
	}

	items, total, err := repo.List(ctx, port.NoteListQuery{
		UserID: "u-1", FolderID: "f-1", Order: port.NoteOrderUpdatedDesc, Limit: 10,
	})
	if err != nil || total != 2 || len(items) != 2 || items[0].ID != "n-2" {
		t.Fatalf("更新时间倒序结果异常: total=%d items=%v err=%v", total, ids(items), err)
	}

	deletedState := entity.NoteStateDeleted
	items, total, err = repo.List(ctx, port.NoteListQuery{
		UserID: "u-1", FolderID: "f-1", State: &deletedState, Order: port.NoteOrderUpdatedDesc,
	})
	if err != nil || total != 1 || items[0].ID != "n-4" {
		t.Fatalf("回收站状态过滤异常: total=%d items=%v err=%v", total, ids(items), err)
	}

	items, _, err = repo.List(ctx, port.NoteListQuery{
		UserID: "u-1", FolderID: "f-1", Order: port.NoteOrderTitleAsc,
	})
	if err != nil || len(items) != 2 || items[0].Title != "Alpha" {
		t.Fatalf("标题升序结果异常: %v err=%v", ids(items), err)
	}

	after := noteNow.Add(time.Minute)
	items, _, err = repo.List(ctx, port.NoteListQuery{
		UserID: "u-1", FolderID: "f-1", Order: port.NoteOrderUpdatedDesc, UpdatedAfter: &after,
	})
	if err != nil || len(items) != 0 {
		t.Fatalf("updatedAfter 应过滤掉等于该时刻的记录: %v err=%v", ids(items), err)
	}

	cursor := &port.NoteCursor{SortKey: port.NoteSortKey(port.NoteOrderUpdatedDesc, second), ID: second.ID}
	items, _, err = repo.List(ctx, port.NoteListQuery{
		UserID: "u-1", FolderID: "f-1", Order: port.NoteOrderUpdatedDesc, Limit: 10, After: cursor,
	})
	if err != nil || len(items) != 1 || items[0].ID != "n-1" {
		t.Fatalf("游标翻页结果异常: %v err=%v", ids(items), err)
	}
}

/* TestNoteRepositoryUpdateAndDelete 覆盖覆盖写入、物理删除与不存在分支 */
func TestNoteRepositoryUpdateAndDelete(t *testing.T) {
	repo := NewNoteRepository(NewStore())
	ctx := context.Background()
	note := newNote(t, "n-1", "u-1", "f-1", "标题", "", noteNow)
	if err := repo.Create(ctx, note); err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}
	if err := note.Save(valueobject.NoteTitle("新标题"), valueobject.NoteContent("正文"), noteNow.Add(time.Minute)); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := repo.Update(ctx, note); err != nil {
		t.Fatalf("更新笔记失败: %v", err)
	}
	stored, _ := repo.GetByID(ctx, "u-1", "n-1")
	if stored.Title != "新标题" || stored.Revision != 2 {
		t.Fatalf("更新未生效: %+v", stored)
	}

	if err := repo.Delete(ctx, "u-1", "n-1"); err != nil {
		t.Fatalf("删除笔记失败: %v", err)
	}
	if _, err := repo.GetByID(ctx, "u-1", "n-1"); err == nil {
		t.Fatal("删除后应返回 RESOURCE_NOT_FOUND")
	}
	if err := repo.Delete(ctx, "u-1", "n-1"); err == nil {
		t.Fatal("删除不存在的笔记应返回错误")
	}
}

/* TestNoteRepositoryFolderBatch 覆盖按目录计数、级联删除、按批次恢复与物理清理 */
func TestNoteRepositoryFolderBatch(t *testing.T) {
	repo := NewNoteRepository(NewStore())
	ctx := context.Background()
	inside := newNote(t, "n-1", "u-1", "f-1", "内", "", noteNow)
	outside := newNote(t, "n-2", "u-1", "f-2", "外", "", noteNow)
	for _, note := range []*entity.Note{inside, outside} {
		if err := repo.Create(ctx, note); err != nil {
			t.Fatalf("写入笔记失败: %v", err)
		}
	}

	counts, err := repo.CountActiveByFolder(ctx, "u-1", []string{"f-1", "f-2", "f-3"})
	if err != nil || counts["f-1"] != 1 || counts["f-2"] != 1 || counts["f-3"] != 0 {
		t.Fatalf("目录计数异常: %v err=%v", counts, err)
	}

	affected, err := repo.MarkDeletedByFolders(ctx, "u-1", []string{"f-1"}, "batch-1", noteNow, time.Hour)
	if err != nil || affected != 1 {
		t.Fatalf("级联软删除异常: affected=%d err=%v", affected, err)
	}
	deleted, _ := repo.GetByID(ctx, "u-1", "n-1")
	if !deleted.IsDeleted() || deleted.DeleteBatchID != "batch-1" || deleted.PurgeAt == nil {
		t.Fatalf("软删除字段异常: %+v", deleted)
	}
	if counts, _ = repo.CountActiveByFolder(ctx, "u-1", []string{"f-1"}); counts["f-1"] != 0 {
		t.Fatalf("回收站笔记不应计入 noteCount: %v", counts)
	}

	restored, err := repo.RestoreByBatch(ctx, "u-1", "batch-1", noteNow.Add(time.Hour))
	if err != nil || restored != 1 {
		t.Fatalf("按批次恢复异常: restored=%d err=%v", restored, err)
	}
	recovered, _ := repo.GetByID(ctx, "u-1", "n-1")
	if recovered.IsDeleted() || recovered.State != entity.NoteStateNormal {
		t.Fatalf("恢复后字段异常: %+v", recovered)
	}
}

/* TestNoteRepositoryRollback 验证事务失败时写入与批处理均回滚 */
func TestNoteRepositoryRollback(t *testing.T) {
	store := NewStore()
	repo := NewNoteRepository(store)
	ctx := context.Background()
	note := newNote(t, "n-1", "u-1", "f-1", "标题", "", noteNow)
	err := store.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := repo.Create(txCtx, note); err != nil {
			return err
		}
		if _, err := repo.MarkDeletedByFolders(txCtx, "u-1", []string{"f-1"}, "batch-1", noteNow, time.Hour); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("事务应返回注入错误")
	}
	if _, err := repo.GetByID(ctx, "u-1", "n-1"); err == nil {
		t.Fatal("事务回滚后不应留下笔记")
	}
}

/* ids 提取笔记 ID 便于失败信息定位 */
func ids(notes []*entity.Note) []string {
	result := make([]string, 0, len(notes))
	for _, note := range notes {
		result = append(result, note.ID)
	}
	return result
}
