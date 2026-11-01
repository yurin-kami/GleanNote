/* 测试目录读取用例：分页与令牌作用域、详情子目录数、面包屑 */
package folder

import (
	"context"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 列表按 pageSize 截断并给出下一页令牌；令牌绑定排序，跨排序复用必须失败 */
func TestListFoldersPaginationAndTokenScope(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	for _, name := range []string{"甲", "乙", "丙"} {
		f.create(t, "u-1", root.ID, name)
	}

	first := f.listQuery(t, ListFoldersInput{UserID: "u-1", ParentID: root.ID, PageSize: 2})
	if len(first.Items) != 2 || first.TotalSize != 3 {
		t.Fatalf("首页条数 = %d、总数 = %d，期望 2 与 3", len(first.Items), first.TotalSize)
	}
	if first.NextPageToken == "" {
		t.Fatal("还有剩余数据时应返回下一页令牌")
	}

	second := f.listQuery(t, ListFoldersInput{
		UserID: "u-1", ParentID: root.ID, PageSize: 2, PageToken: first.NextPageToken,
	})
	if len(second.Items) != 1 || second.Items[0].Name != "丙" || second.NextPageToken != "" {
		t.Fatalf("第二页不符: %+v", second)
	}

	/* 令牌签名覆盖排序方式，换排序再复用同一令牌必须拒绝 */
	_, err := f.service.ListFolders(context.Background(), ListFoldersInput{
		UserID: "u-1", ParentID: root.ID, PageSize: 2,
		PageToken: first.NextPageToken, OrderBy: "name asc",
	})
	requireCode(t, err, errs.CodeInvalidPageToken)

	/* 上限收敛为 100，缺省与负数由用例兜底为 20 */
	if got := f.listQuery(t, ListFoldersInput{UserID: "u-1", ParentID: root.ID, PageSize: 1000}); len(got.Items) != 3 {
		t.Fatalf("超上限应按 100 收敛，实际 %d 条", len(got.Items))
	}
	if got := f.listQuery(t, ListFoldersInput{UserID: "u-1", ParentID: root.ID}); len(got.Items) != 3 {
		t.Fatalf("缺省 pageSize 应返回全部 3 条，实际 %d", len(got.Items))
	}
}

/* 排序白名单之外的取值返回校验错误；父级已删除按 404 处理 */
func TestListFoldersValidation(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	child := f.create(t, "u-1", root.ID, "甲")

	_, err := f.service.ListFolders(context.Background(), ListFoldersInput{
		UserID: "u-1", ParentID: root.ID, OrderBy: "updatedAt desc",
	})
	requireCode(t, err, errs.CodeValidationFailed)

	_, err = f.service.ListFolders(context.Background(), ListFoldersInput{ParentID: root.ID})
	requireCode(t, err, errs.CodeAuthTokenInvalid)

	if _, err := f.service.DeleteFolder(context.Background(), "u-1", root.ID); err != nil {
		t.Fatalf("删除根目录失败: %v", err)
	}
	_, err = f.service.ListFolders(context.Background(), ListFoldersInput{UserID: "u-1", ParentID: root.ID})
	if !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("已删除父级应 404，实际 %v", err)
	}
	/* 已删除目录不得出现在根级列表，删除批次内的子目录同样不可见 */
	if got := f.listQuery(t, ListFoldersInput{UserID: "u-1"}); len(got.Items) != 0 {
		t.Fatalf("根级列表应为空，实际 %d 条（child=%s）", len(got.Items), child.ID)
	}
}

/* 详情返回直接子目录数量；已删除目录按 404 处理 */
func TestGetFolderChildCount(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	f.create(t, "u-1", root.ID, "甲")
	f.create(t, "u-1", root.ID, "乙")

	view := f.currentView(t, "u-1", root.ID)
	if view.ChildCount != 2 || view.Depth != 1 || view.Name != "我的笔记" {
		t.Fatalf("详情不符: %+v", view)
	}

	if _, err := f.service.DeleteFolder(context.Background(), "u-1", root.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	_, err := f.service.GetFolder(context.Background(), "u-1", root.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}

/* 面包屑从根目录逐级下钻，并按「 / 」拼接展示路径 */
func TestFolderPathBreadcrumb(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	math := f.create(t, "u-1", root.ID, "数学")
	linear := f.create(t, "u-1", math.ID, "线性代数")

	path, err := f.service.FolderPath(context.Background(), "u-1", linear.ID)
	if err != nil {
		t.Fatalf("读取面包屑失败: %v", err)
	}
	if len(path.Items) != 3 {
		t.Fatalf("面包屑段数 = %d，期望 3", len(path.Items))
	}
	if path.Items[0].Name != "我的笔记" || path.Items[1].Name != "数学" || path.Items[2].Name != "线性代数" {
		t.Fatalf("面包屑顺序不符: %+v", path.Items)
	}
	if path.DisplayPath != "我的笔记 / 数学 / 线性代数" {
		t.Fatalf("展示路径不符: %s", path.DisplayPath)
	}
}
