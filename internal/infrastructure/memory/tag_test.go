/* 内存标签仓储测试：重名、关联覆盖、计数口径与搜索/回收站读取 */
package memory

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* seedTag 写入标签并要求成功 */
func seedTag(t *testing.T, repo *TagRepository, id, user, name string, now time.Time) *entity.Tag {
	t.Helper()
	parsed, err := valueobject.ParseTagName(name)
	if err != nil {
		t.Fatalf("解析标签名: %v", err)
	}
	tag := entity.NewTag(id, user, parsed, now)
	if err := repo.Create(context.Background(), tag); err != nil {
		t.Fatalf("写入标签: %v", err)
	}
	return tag
}

/* seedNote 写入笔记并回填更新时间 */
func seedNote(t *testing.T, repo *NoteRepository, id, user, folder, title, content string, now time.Time) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	parsedContent, err := valueobject.ParseNoteContent(content)
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	note := entity.NewNote(id, user, folder, parsedTitle, parsedContent, now, now)
	if err := repo.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* TestTagRepositoryRejectsDuplicateName 校验账号内大小写不敏感重名 */
func TestTagRepositoryRejectsDuplicateName(t *testing.T) {
	store := NewStore()
	repo := NewTagRepository(store)
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	seedTag(t, repo, "tag-1", "user-1", "GRE", now)

	parsed, _ := valueobject.ParseTagName("gre")
	err := repo.Create(context.Background(), entity.NewTag("tag-2", "user-1", parsed, now))
	if !errs.IsCode(err, errs.CodeNameDuplicated) {
		t.Fatalf("期望 NAME_DUPLICATED，实际 %v", err)
	}

	/* 其他用户可使用相同名称 */
	seedTag(t, repo, "tag-3", "user-2", "gre", now)
}

/* TestNoteTagRepositoryReplaceAndCount 校验覆盖关联、计数与标签过滤 */
func TestNoteTagRepositoryReplaceAndCount(t *testing.T) {
	store := NewStore()
	tags := NewTagRepository(store)
	notes := NewNoteRepository(store)
	links := NewNoteTagRepository(store, tags, notes)
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

	seedTag(t, tags, "tag-a", "user-1", "数学", now)
	seedTag(t, tags, "tag-b", "user-1", "考研", now)
	note := seedNote(t, notes, "note-1", "user-1", "folder-1", "矩阵的秩", "矩阵的秩等于行阶梯形的非零行数", now)
	seedNote(t, notes, "note-2", "user-1", "folder-1", "线性代数", "无关正文", now)

	ctx := context.Background()
	if err := links.ReplaceForNote(ctx, "user-1", note.ID, []string{"tag-a", "tag-b"}); err != nil {
		t.Fatalf("设置关联: %v", err)
	}
	if err := links.ReplaceForNote(ctx, "user-1", "note-2", []string{"tag-a"}); err != nil {
		t.Fatalf("设置关联: %v", err)
	}

	counts, err := links.CountsByTags(ctx, "user-1", nil)
	if err != nil {
		t.Fatalf("统计标签: %v", err)
	}
	if counts["tag-a"] != 2 || counts["tag-b"] != 1 {
		t.Fatalf("计数不符合预期: %#v", counts)
	}

	ids, err := links.NoteIDsByTags(ctx, "user-1", []string{"tag-a", "tag-b"})
	if err != nil {
		t.Fatalf("按标签过滤: %v", err)
	}
	if len(ids) != 1 || ids[0] != "note-1" {
		t.Fatalf("同时包含语义不符合预期: %#v", ids)
	}

	/* 删除标签只解除关联，不删除笔记 */
	if err := links.DeleteByTag(ctx, "user-1", "tag-b"); err != nil {
		t.Fatalf("删除标签关联: %v", err)
	}
	grouped, err := links.TagsByNotes(ctx, "user-1", []string{"note-1"})
	if err != nil {
		t.Fatalf("读取笔记标签: %v", err)
	}
	if len(grouped["note-1"]) != 1 || grouped["note-1"][0].ID != "tag-a" {
		t.Fatalf("解除关联后剩余标签不符合预期: %#v", grouped["note-1"])
	}
}

/* TestNoteTagCountsExcludeDeleted 校验回收站笔记不计入标签笔记数 */
func TestNoteTagCountsExcludeDeleted(t *testing.T) {
	store := NewStore()
	tags := NewTagRepository(store)
	notes := NewNoteRepository(store)
	links := NewNoteTagRepository(store, tags, notes)
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

	seedTag(t, tags, "tag-a", "user-1", "数学", now)
	note := seedNote(t, notes, "note-1", "user-1", "folder-1", "标题", "正文", now)
	ctx := context.Background()
	if err := links.ReplaceForNote(ctx, "user-1", note.ID, []string{"tag-a"}); err != nil {
		t.Fatalf("设置关联: %v", err)
	}

	note.MarkDeleted("batch-1", now, 30*24*time.Hour)
	if err := notes.Update(ctx, note); err != nil {
		t.Fatalf("软删除笔记: %v", err)
	}
	counts, err := links.CountsByTags(ctx, "user-1", []string{"tag-a"})
	if err != nil {
		t.Fatalf("统计标签: %v", err)
	}
	if counts["tag-a"] != 0 {
		t.Fatalf("回收站笔记不应计数，实际 %d", counts["tag-a"])
	}
}

/* TestNoteSearchPrefersTitle 校验标题命中优先与回收站排除 */
func TestNoteSearchPrefersTitle(t *testing.T) {
	store := NewStore()
	notes := NewNoteRepository(store)
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

	seedNote(t, notes, "note-1", "user-1", "folder-1", "线性代数", "矩阵的秩练习", now.Add(time.Hour))
	seedNote(t, notes, "note-2", "user-1", "folder-1", "矩阵的秩", "正文无关", now)

	deleted := seedNote(t, notes, "note-3", "user-1", "folder-1", "矩阵的秩（旧）", "正文", now)
	deleted.MarkDeleted("batch-1", now, 30*24*time.Hour)
	if err := notes.Update(context.Background(), deleted); err != nil {
		t.Fatalf("软删除笔记: %v", err)
	}

	items, total, err := notes.Search(context.Background(), port.NoteSearchQuery{
		UserID:  "user-1",
		Keyword: "矩阵的秩",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("搜索: %v", err)
	}
	if total != 2 {
		t.Fatalf("命中总数 = %d，期望 2", total)
	}
	if len(items) != 2 || items[0].ID != "note-2" {
		t.Fatalf("标题命中应排在首位: %#v", items)
	}

	deletedRows, err := notes.ListDeleted(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("读取回收站笔记: %v", err)
	}
	if len(deletedRows) != 1 || deletedRows[0].ID != "note-3" {
		t.Fatalf("回收站笔记不符合预期: %#v", deletedRows)
	}
}
