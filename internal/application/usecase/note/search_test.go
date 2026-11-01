/* 笔记搜索用例测试：标题优先、目录子树、标签过滤、摘要转义与分页令牌 */
package note

import (
	"context"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestSearchNotesTitleFirstAndSnippet 校验标题命中排在正文命中之前，摘要只保留 <em> */
func TestSearchNotesTitleFirstAndSnippet(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	math := f.seedFolder(t, "user-1", root, "folder-math", "数学")

	titleHit := f.create(t, "user-1", math.ID, "矩阵的秩", "本文不含关键词")
	contentHit := f.create(t, "user-1", math.ID, "线性代数", "<b>矩阵的秩</b>等于行阶梯形的非零行数")

	result, err := f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "秩",
	})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if result.TotalSize != 2 || len(result.Items) != 2 {
		t.Fatalf("命中数 = %d/%d，期望 2", result.TotalSize, len(result.Items))
	}
	if result.Items[0].NoteID != titleHit.ID {
		t.Fatalf("标题命中未优先: %#v", result.Items)
	}
	if result.Items[1].NoteID != contentHit.ID {
		t.Fatalf("正文命中顺序错误: %#v", result.Items)
	}
	if result.Items[0].FolderPath != "我的笔记 / 数学" {
		t.Fatalf("目录路径 = %q", result.Items[0].FolderPath)
	}
	snippet := result.Items[1].Snippet
	if !strings.Contains(snippet, "<em>秩</em>") {
		t.Fatalf("摘要未高亮关键词: %q", snippet)
	}
	if strings.Contains(snippet, "<b>") || !strings.Contains(snippet, "&lt;b&gt;") {
		t.Fatalf("摘要未转义正文标记: %q", snippet)
	}
}

/* TestSearchNotesFiltersFolderSubtreeAndTags 校验目录含子树与标签「同时包含」语义 */
func TestSearchNotesFiltersFolderSubtreeAndTags(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	math := f.seedFolder(t, "user-1", root, "folder-math", "数学")
	english := f.seedFolder(t, "user-1", root, "folder-english", "英语")

	inMath := f.create(t, "user-1", math.ID, "矩阵笔记", "关键词命中")
	f.create(t, "user-1", english.ID, "语法笔记", "关键词命中")

	result, err := f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", FolderID: root.ID,
	})
	if err != nil {
		t.Fatalf("按根目录搜索失败: %v", err)
	}
	if result.TotalSize != 2 {
		t.Fatalf("根目录子树命中数 = %d，期望 2", result.TotalSize)
	}

	result, err = f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", FolderID: math.ID,
	})
	if err != nil {
		t.Fatalf("按子目录搜索失败: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].NoteID != inMath.ID {
		t.Fatalf("子目录过滤结果 = %#v", result.Items)
	}

	/* 标签过滤为「同时包含」；两篇都有关键词，只有命中全部标签的笔记返回 */
	second := f.create(t, "user-1", math.ID, "另一篇", "关键词命中")
	if err := f.links.ReplaceForNote(context.Background(), "user-1", inMath.ID, []string{"tag-1", "tag-2"}); err != nil {
		t.Fatalf("写入标签关联: %v", err)
	}
	if err := f.links.ReplaceForNote(context.Background(), "user-1", second.ID, []string{"tag-1"}); err != nil {
		t.Fatalf("写入标签关联: %v", err)
	}
	result, err = f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", TagIDs: []string{"tag-1", "tag-2"},
	})
	if err != nil {
		t.Fatalf("按标签搜索失败: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].NoteID != inMath.ID {
		t.Fatalf("标签 AND 过滤结果 = %#v", result.Items)
	}

	/* 标签命中集合为空时直接返回空页，不泄漏其他笔记 */
	result, err = f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", TagIDs: []string{"tag-missing"},
	})
	if err != nil {
		t.Fatalf("搜索未知标签失败: %v", err)
	}
	if len(result.Items) != 0 || result.TotalSize != 0 {
		t.Fatalf("未知标签应返回空页: %#v", result)
	}
}

/* TestSearchNotesPaginatesAndRejectsTamperedToken 校验键集翻页与令牌校验 */
func TestSearchNotesPaginatesAndRejectsTamperedToken(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	for index := 0; index < 3; index++ {
		f.create(t, "user-1", root.ID, "关键词笔记", "正文")
	}

	first, err := f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", PageSize: 1,
	})
	if err != nil {
		t.Fatalf("首页搜索失败: %v", err)
	}
	if len(first.Items) != 1 || first.NextPageToken == "" || first.TotalSize != 3 {
		t.Fatalf("首页结果不符合预期: %#v", first)
	}

	second, err := f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "关键词", PageSize: 2, PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("第二页搜索失败: %v", err)
	}
	if len(second.Items) != 2 || second.NextPageToken != "" {
		t.Fatalf("第二页结果不符合预期: %#v", second)
	}

	/* 同一令牌换关键词后作用域不匹配，必须拒绝 */
	if _, err := f.service.SearchNotes(context.Background(), SearchNotesInput{
		UserID: "user-1", Query: "另一个", PageToken: first.NextPageToken,
	}); !errs.IsCode(err, errs.CodeInvalidPageToken) {
		t.Fatalf("期望 INVALID_PAGE_TOKEN，实际 %v", err)
	}
}
