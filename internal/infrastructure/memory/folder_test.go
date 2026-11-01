/* 测试内存目录仓储：同级重名、可见性过滤、键集分页、子树与批次删除 */
package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* folderNow 是仓储测试的固定基准时间，便于断言排序键 */
var folderNow = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

/* newFolder 构造挂在 parent 下的节点；parent 为 nil 表示根级 */
func newFolder(t *testing.T, id, userID string, parent *entity.Folder, name string, now time.Time) *entity.Folder {
	t.Helper()
	parsed, err := valueobject.ParseFolderName(name)
	if err != nil {
		t.Fatalf("解析目录名: %v", err)
	}
	return entity.NewFolder(id, userID, parent, parsed, now)
}

/* 同一父级下大小写不同的重名必须拒绝；不同父级允许同名 */
func TestFolderRepositoryCreateRejectsDuplicateName(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	if err := repo.Create(ctx, newFolder(t, "f-2", "u-1", root, "Notes", folderNow)); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	err := repo.Create(ctx, newFolder(t, "f-3", "u-1", root, "notes", folderNow))
	if !errs.IsCode(err, errs.CodeNameDuplicated) {
		t.Fatalf("同级重名应返回 NAME_DUPLICATED，实际 %v", err)
	}

	other := newFolder(t, "f-4", "u-1", root, "其他", folderNow)
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("创建另一父级失败: %v", err)
	}
	if err := repo.Create(ctx, newFolder(t, "f-5", "u-1", other, "notes", folderNow)); err != nil {
		t.Fatalf("不同父级同名应允许: %v", err)
	}
}

/* 回收站中的同名节点不占用名称空间，允许新建同名目录 */
func TestFolderRepositoryCreateIgnoresDeletedName(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	trashed := newFolder(t, "f-2", "u-1", root, "笔记", folderNow)
	trashed.MarkDeleted("batch-1", folderNow, time.Hour)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	if err := repo.Create(ctx, trashed); err != nil {
		t.Fatalf("创建已删除节点失败: %v", err)
	}
	if err := repo.Create(ctx, newFolder(t, "f-3", "u-1", root, "笔记", folderNow)); err != nil {
		t.Fatalf("已删除节点不应占用名称: %v", err)
	}
}

/* GetByID 返回含已删除的原始行；跨用户一律 404 */
func TestFolderRepositoryGetByIDVisibility(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	root.MarkDeleted("batch-1", folderNow, time.Hour)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	found, err := repo.GetByID(ctx, "u-1", "f-1")
	if err != nil {
		t.Fatalf("读取已删除节点失败: %v", err)
	}
	if !found.IsDeleted() {
		t.Fatal("原始行应保留删除标记")
	}
	if _, err := repo.GetByID(ctx, "u-2", "f-1"); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("跨用户读取应 404，实际 %v", err)
	}
}

/* ListChildren 只返回未删除节点，并同时给出全量总数与本页条数 */
func TestFolderRepositoryListChildrenPagination(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	for index, name := range []string{"乙", "甲", "丙"} {
		node := newFolder(t, "f-"+string(rune('2'+index)), "u-1", root, name,
			folderNow.Add(time.Duration(index)*time.Minute))
		if err := repo.Create(ctx, node); err != nil {
			t.Fatalf("创建子目录失败: %v", err)
		}
	}

	first, total, err := repo.ListChildren(ctx, port.FolderListQuery{
		UserID: "u-1", ParentID: &root.ID, Order: port.FolderOrderCreatedAtAsc, Limit: 2,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 3 || len(first) != 2 {
		t.Fatalf("总数 = %d，本页 = %d，期望 3 与 2", total, len(first))
	}
	if first[0].Name != "乙" || first[1].Name != "甲" {
		t.Fatalf("创建时间顺序不符: %s %s", first[0].Name, first[1].Name)
	}

	last := first[len(first)-1]
	second, _, err := repo.ListChildren(ctx, port.FolderListQuery{
		UserID: "u-1", ParentID: &root.ID, Order: port.FolderOrderCreatedAtAsc, Limit: 2,
		After: &port.FolderCursor{SortKey: port.FolderSortKey(port.FolderOrderCreatedAtAsc, last), ID: last.ID},
	})
	if err != nil {
		t.Fatalf("翻页失败: %v", err)
	}
	if len(second) != 1 || second[0].Name != "丙" {
		t.Fatalf("第二页不符: %+v", second)
	}

	/* 名称升序按小写比较，中文按字节序，仅验证排序稳定与总数 */
	byName, _, err := repo.ListChildren(ctx, port.FolderListQuery{
		UserID: "u-1", ParentID: &root.ID, Order: port.FolderOrderNameAsc,
	})
	if err != nil {
		t.Fatalf("名称排序失败: %v", err)
	}
	if len(byName) != 3 {
		t.Fatalf("名称排序条数 = %d，期望 3", len(byName))
	}
}

/* CountChildren 只统计未删除的直接子目录 */
func TestFolderRepositoryCountChildren(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	child := newFolder(t, "f-2", "u-1", root, "存活", folderNow)
	trashed := newFolder(t, "f-3", "u-1", root, "回收站", folderNow)
	trashed.MarkDeleted("batch-1", folderNow, time.Hour)
	for _, node := range []*entity.Folder{child, trashed} {
		if err := repo.Create(ctx, node); err != nil {
			t.Fatalf("创建子目录失败: %v", err)
		}
	}

	counts, err := repo.CountChildren(ctx, "u-1", []string{root.ID, child.ID})
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if counts[root.ID] != 1 {
		t.Fatalf("根目录子节点数 = %d，期望 1", counts[root.ID])
	}
	if _, exists := counts[child.ID]; exists {
		t.Fatal("无子节点的目录不应出现在结果中")
	}
}

/* ListSubtree 返回整棵子树并按深度、创建时间排序 */
func TestFolderRepositoryListSubtree(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	child := newFolder(t, "f-2", "u-1", root, "数学", folderNow.Add(time.Minute))
	grand := newFolder(t, "f-3", "u-1", child, "线性代数", folderNow.Add(2*time.Minute))
	other := newFolder(t, "f-4", "u-1", nil, "无关", folderNow)
	for _, node := range []*entity.Folder{child, grand, other} {
		if err := repo.Create(ctx, node); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
	}

	nodes, err := repo.ListSubtree(ctx, "u-1", root.ID)
	if err != nil {
		t.Fatalf("查询子树失败: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("子树节点数 = %d，期望 3", len(nodes))
	}
	if nodes[0].ID != root.ID || nodes[1].ID != child.ID || nodes[2].ID != grand.ID {
		t.Fatalf("子树顺序不符: %s %s %s", nodes[0].ID, nodes[1].ID, nodes[2].ID)
	}
}

/* DeleteBatch 只物理删除同批次且位于子树内的节点 */
func TestFolderRepositoryDeleteBatch(t *testing.T) {
	repo := NewFolderRepository(NewStore())
	ctx := context.Background()

	root := entity.NewRootFolder("f-1", "u-1", folderNow)
	child := newFolder(t, "f-2", "u-1", root, "数学", folderNow)
	kept := newFolder(t, "f-3", "u-1", root, "保留", folderNow)
	if err := repo.Create(ctx, root); err != nil {
		t.Fatalf("创建根目录失败: %v", err)
	}
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}
	if err := repo.Create(ctx, kept); err != nil {
		t.Fatalf("创建保留节点失败: %v", err)
	}

	deletedChild := cloneFolder(child)
	deletedChild.MarkDeleted("batch-1", folderNow, time.Hour)
	if err := repo.SaveBatch(ctx, []*entity.Folder{deletedChild}); err != nil {
		t.Fatalf("写入删除批次失败: %v", err)
	}

	removed, err := repo.DeleteBatch(ctx, "u-1", child.ID)
	if err != nil {
		t.Fatalf("彻底删除失败: %v", err)
	}
	if removed != 1 {
		t.Fatalf("删除数量 = %d，期望 1", removed)
	}
	if _, err := repo.GetByID(ctx, "u-1", child.ID); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("节点应被物理删除，实际 %v", err)
	}
	if _, err := repo.GetByID(ctx, "u-1", kept.ID); err != nil {
		t.Fatalf("同父级其他节点不应被删除: %v", err)
	}
}

/* 事务失败时新建的目录必须撤销，仓储内不可见 */
func TestFolderRepositoryCreateRollback(t *testing.T) {
	store := NewStore()
	repo := NewFolderRepository(store)
	ctx := context.Background()

	err := store.WithinTransaction(ctx, func(tx context.Context) error {
		if err := repo.Create(tx, entity.NewRootFolder("f-1", "u-1", folderNow)); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("应返回事务错误")
	}
	if _, err := repo.GetByID(ctx, "u-1", "f-1"); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("回滚后目录不应存在: %v", err)
	}
}
