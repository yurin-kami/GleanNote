/* 测试目录写入用例：层级与重名约束、乐观锁改名、移动环检测与路径重写 */
package folder

import (
	"context"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 新目录最多第 5 层；父级不可见或名称非法必须立即失败 */
func TestCreateFolderConstraints(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")

	parent := FolderView{ID: root.ID, Depth: root.Depth}
	for depth := 2; depth <= entity.MaxFolderDepth; depth++ {
		view := f.create(t, "u-1", parent.ID, "第"+string(rune('0'+depth))+"层")
		if view.Depth != depth {
			t.Fatalf("深度 = %d，期望 %d", view.Depth, depth)
		}
		parent = view
	}

	_, err := f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID: "u-1", ParentID: parent.ID, Name: "第六层",
	})
	requireCode(t, err, errs.CodeFolderDepthExceeded)

	_, err = f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID: "u-1", ParentID: "missing", Name: "任意",
	})
	requireCode(t, err, errs.CodeResourceNotFound)

	_, err = f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID: "u-1", ParentID: root.ID, Name: "a/b",
	})
	requireCode(t, err, errs.CodeValidationFailed)
}

/* 同级名称大小写不敏感唯一；根级同名同样被拒绝 */
func TestCreateFolderDuplicateName(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	f.create(t, "u-1", root.ID, "Notes")

	_, err := f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID: "u-1", ParentID: root.ID, Name: "notes",
	})
	requireCode(t, err, errs.CodeNameDuplicated)

	_, err = f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID: "u-1", Name: entity.RootFolderName,
	})
	requireCode(t, err, errs.CodeNameDuplicated)
}

/* 改名必须匹配修订号；同名不推进修订号；与同级重名返回 409 */
func TestRenameFolderRevisionAndDuplicate(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	first := f.create(t, "u-1", root.ID, "甲")
	second := f.create(t, "u-1", root.ID, "乙")

	renamed, err := f.service.RenameFolder(context.Background(), RenameFolderInput{
		UserID: "u-1", FolderID: first.ID, Name: "丙", Revision: 1,
	})
	if err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if renamed.Name != "丙" || renamed.Revision != 2 {
		t.Fatalf("改名结果不符: %+v", renamed)
	}

	_, err = f.service.RenameFolder(context.Background(), RenameFolderInput{
		UserID: "u-1", FolderID: first.ID, Name: "丁", Revision: 1,
	})
	requireCode(t, err, errs.CodeRevisionMismatch)

	same, err := f.service.RenameFolder(context.Background(), RenameFolderInput{
		UserID: "u-1", FolderID: first.ID, Name: "丙", Revision: 2,
	})
	if err != nil || same.Revision != 2 {
		t.Fatalf("同名应为无操作且不推进修订号: %+v %v", same, err)
	}

	_, err = f.service.RenameFolder(context.Background(), RenameFolderInput{
		UserID: "u-1", FolderID: second.ID, Name: "丙", Revision: 1,
	})
	requireCode(t, err, errs.CodeNameDuplicated)
}

/* 移动后子树路径与深度整体平移，根级移动不需要目标父级 */
func TestMoveFolderRewritesSubtree(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "目标")
	source := f.create(t, "u-1", root.ID, "来源")
	leaf := f.create(t, "u-1", source.ID, "子项")

	moved, err := f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: source.ID, TargetParentID: target.ID,
	})
	if err != nil {
		t.Fatalf("移动失败: %v", err)
	}
	if moved.MovedFolderCount != 2 {
		t.Fatalf("受影响目录数 = %d，期望 2", moved.MovedFolderCount)
	}
	if moved.Folder.ParentID == nil || *moved.Folder.ParentID != target.ID || moved.Folder.Depth != 3 {
		t.Fatalf("移动后父级或深度不符: %+v", moved.Folder)
	}

	leafNode, err := f.folders.GetByID(context.Background(), "u-1", leaf.ID)
	if err != nil {
		t.Fatalf("读取子项失败: %v", err)
	}
	if want := root.Path + "/" + target.ID + "/" + source.ID + "/" + leaf.ID; leafNode.Path != want {
		t.Fatalf("子项路径 = %s，期望 %s", leafNode.Path, want)
	}
	if leafNode.Depth != 4 {
		t.Fatalf("子项深度 = %d，期望 4", leafNode.Depth)
	}

	back, err := f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: source.ID,
	})
	if err != nil {
		t.Fatalf("移动到根级失败: %v", err)
	}
	if back.Folder.ParentID != nil || back.Folder.Depth != 1 {
		t.Fatalf("根级移动结果不符: %+v", back.Folder)
	}
}

/* 移动到自身或子孙构成环；超过深度上限同样拒绝 */
func TestMoveFolderRejectsCycleAndDepth(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	outer := f.create(t, "u-1", root.ID, "外层")
	inner := f.create(t, "u-1", outer.ID, "内层")

	_, err := f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: outer.ID, TargetParentID: inner.ID,
	})
	requireCode(t, err, errs.CodeFolderCycle)

	_, err = f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: outer.ID, TargetParentID: outer.ID,
	})
	requireCode(t, err, errs.CodeFolderCycle)

	deep := FolderView{ID: root.ID, Depth: root.Depth}
	for depth := 2; depth <= entity.MaxFolderDepth; depth++ {
		deep = f.create(t, "u-1", deep.ID, "层"+string(rune('0'+depth)))
	}

	/* 外层子树高 1，挂到第 5 层父级后总高 6，必须拒绝 */
	_, err = f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: outer.ID, TargetParentID: deep.ID,
	})
	requireCode(t, err, errs.CodeFolderDepthExceeded)
}

/* 目标父级下已有同名目录时拒绝移动，避免破坏同级唯一约束 */
func TestMoveFolderDuplicateName(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "u-1")
	target := f.create(t, "u-1", root.ID, "目标")
	f.create(t, "u-1", target.ID, "冲突")
	moving := f.create(t, "u-1", root.ID, "冲突")

	_, err := f.service.MoveFolder(context.Background(), MoveFolderInput{
		UserID: "u-1", FolderID: moving.ID, TargetParentID: target.ID,
	})
	requireCode(t, err, errs.CodeNameDuplicated)
}
