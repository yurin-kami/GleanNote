/* 回收站用例测试：批次聚合、类型判定、剩余天数与分页 */
package trash

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* trashFixture 汇总回收站用例所需依赖 */
type trashFixture struct {
	service *Service
	clock   *clock.Fixed
	folders *memory.FolderRepository
	notes   *memory.NoteRepository
}

/* newTrashFixture 装配回收站服务；时间固定为 2026-10-28T06:00:00Z */
func newTrashFixture(t *testing.T) *trashFixture {
	t.Helper()
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	folders := memory.NewFolderRepository(store)
	notes := memory.NewNoteRepository(store)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	service, err := NewService(Deps{Clock: fixed, Folders: folders, Notes: notes, PageTokens: codec})
	if err != nil {
		t.Fatalf("创建回收站服务: %v", err)
	}
	return &trashFixture{service: service, clock: fixed, folders: folders, notes: notes}
}

/* seedFolder 写入目录并按要求移入回收站 */
func (f *trashFixture) seedFolder(t *testing.T, userID string, parent *entity.Folder, id, name, batch string, now time.Time) *entity.Folder {
	t.Helper()
	parsed, err := valueobject.ParseFolderName(name)
	if err != nil {
		t.Fatalf("解析目录名: %v", err)
	}
	folder := entity.NewFolder(id, userID, parent, parsed, now)
	if batch != "" {
		folder.MarkDeleted(batch, now, 30*24*time.Hour)
	}
	if err := f.folders.Create(context.Background(), folder); err != nil {
		t.Fatalf("写入目录: %v", err)
	}
	return folder
}

/* seedNote 写入笔记并按需软删除 */
func (f *trashFixture) seedNote(t *testing.T, userID, folderID, id, title, batch string, now time.Time) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	content, err := valueobject.ParseNoteContent("正文")
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	note := entity.NewNote(id, userID, folderID, parsedTitle, content, now, now)
	if batch != "" {
		note.MarkDeleted(batch, now, 30*24*time.Hour)
	}
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* TestListTrashAggregatesBatches 校验目录批次与笔记批次的聚合字段 */
func TestListTrashAggregatesBatches(t *testing.T) {
	f := newTrashFixture(t)
	user := "user-1"
	root := f.seedFolder(t, user, nil, "root", "我的笔记", "", f.clock.Now())
	now := f.clock.Now()

	/* 目录批次：与子树内笔记共用批次，删除时间较早 */
	f.seedFolder(t, user, root, "folder-math", "数学", "batch-folder", now)
	f.seedNote(t, user, "folder-math", "note-in-folder", "矩阵的秩", "batch-folder", now)

	/* 笔记批次：单独删除，时间较晚，应排在目录批次之前 */
	later := now.Add(time.Minute)
	f.seedNote(t, user, root.ID, "note-alone", "线性代数", "batch-note", later)

	result, err := f.service.ListTrash(context.Background(), ListTrashInput{UserID: user})
	if err != nil {
		t.Fatalf("列出回收站: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("条目数 = %d，期望 2", len(result.Items))
	}
	first := result.Items[0]
	if first.ItemType != TrashItemNote || first.ItemID != "note-alone" || first.NoteCount != 1 {
		t.Fatalf("笔记批次不符合预期: %#v", first)
	}
	second := result.Items[1]
	if second.ItemType != TrashItemFolder || second.ItemID != "folder-math" ||
		second.FolderCount != 1 || second.NoteCount != 1 {
		t.Fatalf("目录批次不符合预期: %#v", second)
	}
	if second.OriginalPath != "我的笔记 / 数学" {
		t.Fatalf("原始路径 = %q，期望 我的笔记 / 数学", second.OriginalPath)
	}
	if second.RemainingDays != 30 {
		t.Fatalf("剩余天数 = %d，期望 30", second.RemainingDays)
	}
}

/* TestListTrashPagination 校验 pageSize=1 时按删除时间倒序翻页 */
func TestListTrashPagination(t *testing.T) {
	f := newTrashFixture(t)
	user := "user-1"
	root := f.seedFolder(t, user, nil, "root", "我的笔记", "", f.clock.Now())
	now := f.clock.Now()
	f.seedNote(t, user, root.ID, "note-1", "第一篇", "batch-1", now)
	f.seedNote(t, user, root.ID, "note-2", "第二篇", "batch-2", now.Add(time.Minute))

	seen := make([]string, 0, 2)
	token := ""
	for {
		page, err := f.service.ListTrash(context.Background(), ListTrashInput{
			UserID: user, PageSize: 1, PageToken: token,
		})
		if err != nil {
			t.Fatalf("分页查询: %v", err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.Name)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	if fmt.Sprintf("%v", seen) != "[第二篇 第一篇]" {
		t.Fatalf("回收站翻页顺序 = %v", seen)
	}

	if _, err := f.service.ListTrash(context.Background(), ListTrashInput{
		UserID: user, PageToken: "tampered",
	}); !errs.IsCode(err, errs.CodeInvalidPageToken) {
		t.Fatalf("期望 INVALID_PAGE_TOKEN，实际 %v", err)
	}
}
