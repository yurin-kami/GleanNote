/* 测试目录回收站用例：整棵子树软删除、按批次恢复、回落到根级与彻底删除 */
package folder

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 软删除整棵子树并给出批次摘要；已删除目录再次删除按 404 处理 */
func TestDeleteFolderMarksSubtree(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "待删除")
	f.create(t, "u-1", target.ID, "子项")
	kept := f.create(t, "u-1", root.ID, "保留")

	result, err := f.service.DeleteFolder(context.Background(), "u-1", target.ID)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if result.FolderCount != 2 || result.DeleteBatchID == "" {
		t.Fatalf("删除摘要不符: %+v", result)
	}
	if want := f.clock.Now().Add(TrashRetention); !result.PurgeAt.Equal(want) {
		t.Fatalf("到期时间 = %s，期望 %s", result.PurgeAt, want)
	}

	_, err = f.service.GetFolder(context.Background(), "u-1", target.ID)
	requireCode(t, err, errs.CodeResourceNotFound)

	remaining := f.listQuery(t, ListFoldersInput{UserID: "u-1", ParentID: root.ID})
	if len(remaining.Items) != 1 || remaining.Items[0].ID != kept.ID {
		t.Fatalf("删除后同级应只剩保留项: %+v", remaining.Items)
	}

	_, err = f.service.DeleteFolder(context.Background(), "u-1", target.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}

/* 父级仍可见时按原位置恢复，同批次子节点一并回到原树 */
func TestRestoreFolderToParent(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "数学")
	f.create(t, "u-1", target.ID, "线性代数")
	if _, err := f.service.DeleteFolder(context.Background(), "u-1", target.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	view, err := f.service.RestoreFolder(context.Background(), "u-1", target.ID)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if view.RestoredToRoot {
		t.Fatal("父级可见时不应回落到根级")
	}
	if view.ParentID == nil || *view.ParentID != root.ID || view.ChildCount != 1 {
		t.Fatalf("恢复结果不符: %+v", view)
	}

	_, err = f.service.RestoreFolder(context.Background(), "u-1", target.ID)
	requireCode(t, err, errs.CodePreconditionFailed)
}

/* 原父级仍在回收站时整棵恢复到根级，并显式返回 restoredToRoot */
func TestRestoreFallsBackToRoot(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "数学")
	leaf := f.create(t, "u-1", target.ID, "线性代数")

	if _, err := f.service.DeleteFolder(context.Background(), "u-1", root.ID); err != nil {
		t.Fatalf("删除根目录失败: %v", err)
	}
	view, err := f.service.RestoreFolder(context.Background(), "u-1", target.ID)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if !view.RestoredToRoot || view.ParentID != nil || view.Depth != 1 {
		t.Fatalf("应回落到根级: %+v", view)
	}

	node, err := f.folders.GetByID(context.Background(), "u-1", leaf.ID)
	if err != nil {
		t.Fatalf("读取子项失败: %v", err)
	}
	if node.IsDeleted() || node.Depth != 2 || node.Path != "/"+target.ID+"/"+leaf.ID {
		t.Fatalf("子项应同步恢复并重写路径: %+v", node)
	}
}

/* 恢复时同级重名追加「 (2)」并推进修订号 */
func TestRestoreRenamesDuplicate(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	original := f.create(t, "u-1", root.ID, "资料")
	if _, err := f.service.DeleteFolder(context.Background(), "u-1", original.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	f.create(t, "u-1", root.ID, "资料")

	view, err := f.service.RestoreFolder(context.Background(), "u-1", original.ID)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if view.Name != "资料 (2)" || view.Revision != 2 {
		t.Fatalf("恢复改名不符: %+v", view)
	}
}

/* 到期清理只彻底删除 purge_at 已过的目录，未到期保持可恢复 */
func TestPurgeExpiredFolders(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	expired := f.create(t, "u-1", root.ID, "到期")
	fresh := f.create(t, "u-1", root.ID, "未到期")
	for _, id := range []string{expired.ID, fresh.ID} {
		if _, err := f.service.DeleteFolder(context.Background(), "u-1", id); err != nil {
			t.Fatalf("删除目录失败: %v", err)
		}
	}

	/* 只让第一篇到期：把它的 purge_at 提前，另一篇保持原保留期 */
	stored, err := f.folders.GetByID(context.Background(), "u-1", expired.ID)
	if err != nil {
		t.Fatalf("读取到期目录: %v", err)
	}
	early := f.clock.Now().Add(time.Minute)
	stored.PurgeAt = &early
	if err := f.folders.Update(context.Background(), stored); err != nil {
		t.Fatalf("调整到期时间: %v", err)
	}
	f.clock.Advance(2 * time.Minute)

	purged, err := f.service.PurgeExpiredFolders(context.Background(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("到期清理结果 = %d, err=%v", purged, err)
	}
	_, err = f.folders.GetByID(context.Background(), "u-1", expired.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
	if _, err := f.service.RestoreFolder(context.Background(), "u-1", fresh.ID); err != nil {
		t.Fatalf("未到期目录应可恢复: %v", err)
	}
}

/* 彻底删除只能作用于回收站目录，并物理移除同批次子树 */
func TestExpungeFolder(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "待清空")
	f.create(t, "u-1", target.ID, "子项")

	err := f.service.ExpungeFolder(context.Background(), "u-1", target.ID)
	requireCode(t, err, errs.CodePreconditionFailed)

	if _, err := f.service.DeleteFolder(context.Background(), "u-1", target.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if err := f.service.ExpungeFolder(context.Background(), "u-1", target.ID); err != nil {
		t.Fatalf("彻底删除失败: %v", err)
	}
	_, err = f.folders.GetByID(context.Background(), "u-1", target.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}
