/* 笔记读取用例测试：创建、详情、分页、状态与时间过滤、参数校验 */
package note

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestCreateAndGetNote 覆盖创建字段、目录路径与不可见目录 */
func TestCreateAndGetNote(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	sub := f.seedFolder(t, "u-1", root, "f-1", "数学")

	view := f.create(t, "u-1", sub.ID, "矩阵的秩", "正文内容")
	if view.State != string(entity.NoteStateNormal) || view.Revision != 1 || view.CharCount != 4 {
		t.Fatalf("创建视图字段异常: %+v", view)
	}
	if view.FolderPath != "我的笔记 / 数学" {
		t.Fatalf("folderPath = %q", view.FolderPath)
	}

	detail, err := f.service.GetNote(context.Background(), "u-1", view.ID)
	if err != nil || detail.Content != "正文内容" || detail.Title != "矩阵的秩" {
		t.Fatalf("详情字段异常: %+v err=%v", detail, err)
	}

	_, err = f.service.CreateNote(context.Background(), CreateNoteInput{
		UserID: "u-1", FolderID: "missing-folder", Title: "x",
	})
	requireCode(t, err, errs.CodeResourceNotFound)

	_, err = f.service.CreateNote(context.Background(), CreateNoteInput{UserID: "u-1", FolderID: ""})
	requireCode(t, err, errs.CodeValidationFailed)
}

/* TestListNotes 覆盖默认排序、分页令牌、状态过滤与标题排序 */
func TestListNotes(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	first := f.create(t, "u-1", root.ID, "Alpha", "")
	f.clock.Advance(time.Minute)
	second := f.create(t, "u-1", root.ID, "beta", "")
	f.clock.Advance(time.Minute)
	third := f.create(t, "u-1", root.ID, "Gamma", "")

	page, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "u-1", FolderID: root.ID, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if page.TotalSize != 3 || len(page.Items) != 2 || page.Items[0].ID != third.ID || page.Items[1].ID != second.ID {
		t.Fatalf("默认更新时间倒序异常: %+v", page.Items)
	}
	if page.NextPageToken == "" {
		t.Fatal("首页应返回 nextPageToken")
	}

	next, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "u-1", FolderID: root.ID, PageSize: 2, PageToken: page.NextPageToken,
	})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != first.ID || next.NextPageToken != "" {
		t.Fatalf("第二页异常: %+v err=%v", next.Items, err)
	}

	if _, err := f.service.DeleteNote(context.Background(), "u-1", second.ID); err != nil {
		t.Fatalf("删除笔记失败: %v", err)
	}
	active, err := f.service.ListNotes(context.Background(), ListNotesInput{UserID: "u-1", FolderID: root.ID})
	if err != nil || active.TotalSize != 2 {
		t.Fatalf("默认应排除回收站: total=%d err=%v", active.TotalSize, err)
	}
	trash, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "u-1", FolderID: root.ID, State: string(entity.NoteStateDeleted),
	})
	if err != nil || trash.TotalSize != 1 || trash.Items[0].ID != second.ID {
		t.Fatalf("回收站过滤异常: %+v err=%v", trash.Items, err)
	}

	byTitle, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "u-1", FolderID: root.ID, OrderBy: "title asc",
	})
	if err != nil || byTitle.Items[0].Title != "Alpha" || byTitle.Items[1].Title != "Gamma" {
		t.Fatalf("标题升序异常: %+v err=%v", byTitle.Items, err)
	}

	after := second.UpdatedAt
	refreshed, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "u-1", FolderID: root.ID, UpdatedAfter: after.Format(time.RFC3339),
	})
	if err != nil || refreshed.TotalSize != 1 || refreshed.Items[0].ID != third.ID {
		t.Fatalf("updatedAfter 过滤异常: %+v err=%v", refreshed.Items, err)
	}
}

/* TestListNotesRejectsInvalidParams 覆盖状态、排序、时间与目录参数的非法取值 */
func TestListNotesRejectsInvalidParams(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	cases := []ListNotesInput{
		{UserID: "u-1", FolderID: ""},
		{UserID: "u-1", FolderID: root.ID, State: "ARCHIVED"},
		{UserID: "u-1", FolderID: root.ID, OrderBy: "name asc"},
		{UserID: "u-1", FolderID: root.ID, UpdatedAfter: "2026-10-28"},
	}
	for _, input := range cases {
		_, err := f.service.ListNotes(context.Background(), input)
		requireCode(t, err, errs.CodeValidationFailed)
	}
}

/* TestGetNoteInTrash 验证回收站笔记仍可读且带 DELETED 状态 */
func TestGetNoteInTrash(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	note := f.create(t, "u-1", root.ID, "标题", "正文")
	f.delete(t, "u-1", note.ID)

	stored, err := f.service.GetNote(context.Background(), "u-1", note.ID)
	if err != nil || stored.State != string(entity.NoteStateDeleted) || stored.Content != "正文" {
		t.Fatalf("回收站详情异常: %+v err=%v", stored, err)
	}
}
