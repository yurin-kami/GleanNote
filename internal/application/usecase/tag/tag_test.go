/* 标签用例测试：创建/重名、列表排序与分页、重命名、删除与笔记标签覆盖 */
package tag

import (
	"context"
	"fmt"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestTagCrossAccountNotFound 校验标签与笔记的跨账号越权一律 404 */
func TestTagCrossAccountNotFound(t *testing.T) {
	f := newFixture(t)
	owned := f.createTag(t, "user-1", "考研")
	other := f.createTag(t, "user-2", "数学")
	f.seedNote(t, "note-1", "user-1", "笔记")
	ctx := context.Background()

	requireCode(t, errOf(func() error {
		_, err := f.service.RenameTag(ctx, RenameTagInput{
			UserID: "user-2", TagID: owned.ID, Name: "越权改名",
		})
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		return f.service.DeleteTag(ctx, "user-2", owned.ID)
	}), errs.CodeResourceNotFound)
	/* 用他人的笔记设置自己账号的标签 */
	requireCode(t, errOf(func() error {
		_, err := f.service.SetNoteTags(ctx, SetNoteTagsInput{
			UserID: "user-2", NoteID: "note-1", TagIDs: []string{other.ID},
		})
		return err
	}), errs.CodeResourceNotFound)
	/* 给自己的笔记挂他人账号的标签 */
	requireCode(t, errOf(func() error {
		_, err := f.service.SetNoteTags(ctx, SetNoteTagsInput{
			UserID: "user-1", NoteID: "note-1", TagIDs: []string{other.ID},
		})
		return err
	}), errs.CodeResourceNotFound)

	/* 原账号标签仍然可用 */
	if _, err := f.service.RenameTag(ctx, RenameTagInput{
		UserID: "user-1", TagID: owned.ID, Name: "备考",
	}); err != nil {
		t.Fatalf("原账号重命名标签: %v", err)
	}
}

/* errOf 提取一次调用的错误，便于复用 requireCode 断言 */
func errOf(call func() error) error {
	return call()
}

/* TestCreateTagRejectsDuplicateAndInvalid 校验重名与非法名称 */
func TestCreateTagRejectsDuplicateAndInvalid(t *testing.T) {
	f := newFixture(t)
	f.createTag(t, "user-1", "考研")

	if _, err := f.service.CreateTag(context.Background(), CreateTagInput{UserID: "user-1", Name: "考研"}); !errs.IsCode(err, errs.CodeNameDuplicated) {
		t.Fatalf("期望 NAME_DUPLICATED，实际 %v", err)
	}
	if _, err := f.service.CreateTag(context.Background(), CreateTagInput{UserID: "user-1", Name: ""}); !errs.IsValidation(err) {
		t.Fatalf("期望 VALIDATION_FAILED，实际 %v", err)
	}
}

/* TestListTagsOrdersByNoteCount 校验默认按笔记数倒序，name asc 可与分页组合 */
func TestListTagsOrdersByNoteCount(t *testing.T) {
	f := newFixture(t)
	first := f.createTag(t, "user-1", "数学")
	second := f.createTag(t, "user-1", "考研")
	f.createTag(t, "user-1", "英语")

	f.seedNote(t, "note-1", "user-1", "笔记一")
	f.seedNote(t, "note-2", "user-1", "笔记二")
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: []string{first.ID, second.ID},
	}); err != nil {
		t.Fatalf("设置标签: %v", err)
	}
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-2", TagIDs: []string{first.ID},
	}); err != nil {
		t.Fatalf("设置标签: %v", err)
	}

	result, err := f.service.ListTags(context.Background(), ListTagsInput{UserID: "user-1"})
	if err != nil {
		t.Fatalf("列出标签: %v", err)
	}
	if len(result.Items) != 3 || result.Items[0].ID != first.ID || result.Items[0].NoteCount != 2 {
		t.Fatalf("默认排序不符合预期: %#v", result.Items)
	}

	/* 每页 1 条，按名称升序翻页 */
	seen := make([]string, 0, 3)
	token := ""
	for {
		page, err := f.service.ListTags(context.Background(), ListTagsInput{
			UserID: "user-1", OrderBy: "name asc", PageSize: 1, PageToken: token,
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
	/* 内存实现按 lower(name) 的字节序排序（与 PostgreSQL lower(name) 索引口径一致） */
	if fmt.Sprintf("%v", seen) != "[数学 考研 英语]" {
		t.Fatalf("名称升序翻页结果 = %v", seen)
	}
}

/* TestRenameTagSyncsName 校验重命名后关联笔记读到新名称，重名返回 409 */
func TestRenameTagSyncsName(t *testing.T) {
	f := newFixture(t)
	tagView := f.createTag(t, "user-1", "考研")
	other := f.createTag(t, "user-1", "数学")
	f.seedNote(t, "note-1", "user-1", "笔记")
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: []string{tagView.ID},
	}); err != nil {
		t.Fatalf("设置标签: %v", err)
	}

	renamed, err := f.service.RenameTag(context.Background(), RenameTagInput{
		UserID: "user-1", TagID: tagView.ID, Name: "备考",
	})
	if err != nil {
		t.Fatalf("重命名标签: %v", err)
	}
	if renamed.Name != "备考" || renamed.NoteCount != 1 {
		t.Fatalf("重命名结果不符合预期: %#v", renamed)
	}
	grouped, err := f.links.TagsByNotes(context.Background(), "user-1", []string{"note-1"})
	if err != nil {
		t.Fatalf("读取笔记标签: %v", err)
	}
	if len(grouped["note-1"]) != 1 || grouped["note-1"][0].Name != "备考" {
		t.Fatalf("关联笔记未同步新名称: %#v", grouped["note-1"])
	}

	if _, err := f.service.RenameTag(context.Background(), RenameTagInput{
		UserID: "user-1", TagID: tagView.ID, Name: other.Name,
	}); !errs.IsCode(err, errs.CodeNameDuplicated) {
		t.Fatalf("期望 NAME_DUPLICATED，实际 %v", err)
	}
}

/* TestDeleteTagRemovesLinks 校验删除标签只解除关联 */
func TestDeleteTagRemovesLinks(t *testing.T) {
	f := newFixture(t)
	tagView := f.createTag(t, "user-1", "考研")
	f.seedNote(t, "note-1", "user-1", "笔记")
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: []string{tagView.ID},
	}); err != nil {
		t.Fatalf("设置标签: %v", err)
	}

	if err := f.service.DeleteTag(context.Background(), "user-1", tagView.ID); err != nil {
		t.Fatalf("删除标签: %v", err)
	}
	grouped, err := f.links.TagsByNotes(context.Background(), "user-1", []string{"note-1"})
	if err != nil {
		t.Fatalf("读取笔记标签: %v", err)
	}
	if len(grouped["note-1"]) != 0 {
		t.Fatalf("删除标签后仍有残留关联: %#v", grouped["note-1"])
	}
	if err := f.service.DeleteTag(context.Background(), "user-1", tagView.ID); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("重复删除期望 RESOURCE_NOT_FOUND，实际 %v", err)
	}
}

/* TestSetNoteTagsLimitAndMissing 校验超过 10 个与不存在标签的错误码 */
func TestSetNoteTagsLimitAndMissing(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "笔记")
	ids := make([]string, 0, 11)
	for index := 0; index < 11; index++ {
		ids = append(ids, f.createTag(t, "user-1", fmt.Sprintf("标签%d", index)).ID)
	}
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: ids,
	}); !errs.IsCode(err, errs.CodeTagLimitExceeded) {
		t.Fatalf("期望 TAG_LIMIT_EXCEEDED，实际 %v", err)
	}
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: []string{"missing"},
	}); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("期望 RESOURCE_NOT_FOUND，实际 %v", err)
	}

	/* 去重后 10 个合法标签可通过 */
	if _, err := f.service.SetNoteTags(context.Background(), SetNoteTagsInput{
		UserID: "user-1", NoteID: "note-1", TagIDs: ids[:10],
	}); err != nil {
		t.Fatalf("设置 10 个标签: %v", err)
	}
}
