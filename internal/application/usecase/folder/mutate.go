/* 目录写入用例：新建、改名与移动 */
package folder

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * CreateFolder 在指定父级下新建目录。
 * 约束：父级必须可见且 depth ≤ 4（新目录最多为第 5 层）；同级名称唯一。
 * 参考：docs/API.md#523-post-v1folders、PRD FR-02
 */
func (s *Service) CreateFolder(ctx context.Context, input CreateFolderInput) (FolderView, error) {
	if err := requireUser(input.UserID); err != nil {
		return FolderView{}, err
	}
	name, err := valueobject.ParseFolderName(input.Name)
	if err != nil {
		return FolderView{}, err
	}
	now := s.clock.Now()
	var view FolderView
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		/* 目录树写入串行化：新建也要取树锁，避免与并发移动交错产生断链路径 */
		if err := s.deps.Folders.LockTree(txCtx, input.UserID); err != nil {
			return err
		}
		var parent *entity.Folder
		if input.ParentID != "" {
			found, err := s.activeFolder(txCtx, input.UserID, input.ParentID)
			if err != nil {
				return err
			}
			if found.Depth >= entity.MaxFolderDepth {
				return errs.New(errs.CodeFolderDepthExceeded, "父目录已是第 5 层")
			}
			parent = found
		}
		duplicated, err := s.deps.Folders.NameExists(txCtx, input.UserID, parentIDOf(parent), name.String(), "")
		if err != nil {
			return err
		}
		if duplicated {
			return errs.New(errs.CodeNameDuplicated, "同级目录已存在同名目录")
		}
		id, err := s.newID()
		if err != nil {
			return err
		}
		folder := entity.NewFolder(id, input.UserID, parent, name, now)
		if err := s.deps.Folders.Create(txCtx, folder); err != nil {
			return err
		}
		view = toView(folder)
		return nil
	})
	if err != nil {
		return FolderView{}, err
	}
	return view, nil
}

/*
 * RenameFolder 按修订号乐观锁改名。
 * 修订号不匹配返回 412；同名视为无操作，不推进修订号。
 * 参考：docs/API.md#525-patch-v1foldersfolderid、ADR-013
 */
func (s *Service) RenameFolder(ctx context.Context, input RenameFolderInput) (FolderView, error) {
	if err := requireUser(input.UserID); err != nil {
		return FolderView{}, err
	}
	name, err := valueobject.ParseFolderName(input.Name)
	if err != nil {
		return FolderView{}, err
	}
	now := s.clock.Now()
	var view FolderView
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.Folders.LockTree(txCtx, input.UserID); err != nil {
			return err
		}
		folder, err := s.activeFolder(txCtx, input.UserID, input.FolderID)
		if err != nil {
			return err
		}
		if folder.Revision != input.Revision {
			return errs.New(errs.CodeRevisionMismatch, "目录版本已变化")
		}
		if folder.Name != name.String() {
			duplicated, err := s.deps.Folders.NameExists(txCtx, input.UserID, folder.ParentID, name.String(), folder.ID)
			if err != nil {
				return err
			}
			if duplicated {
				return errs.New(errs.CodeNameDuplicated, "同级目录已存在同名目录")
			}
			if err := folder.Rename(name, input.Revision, now); err != nil {
				return err
			}
			/* 条件更新：并发改名在树锁外发生时必须失败，不能静默覆盖 */
			applied, err := s.deps.Folders.UpdateWithRevision(txCtx, folder, input.Revision)
			if err != nil {
				return err
			}
			if !applied {
				return errs.New(errs.CodeRevisionMismatch, "目录版本已变化")
			}
		}
		view = toView(folder)
		return nil
	})
	if err != nil {
		return FolderView{}, err
	}
	view.ChildCount, err = s.countChildren(ctx, input.UserID, input.FolderID)
	if err != nil {
		return FolderView{}, err
	}
	return view, nil
}

/*
 * MoveFolder 把目录及其子树移动到目标父级。
 * 校验顺序：目标可见 → 禁止移到自身或子孙 → 深度上限 → 目标父级重名 → 批量重写路径。
 * 参考：docs/API.md#526-post-v1foldersfolderidmove、ADR-013
 */
func (s *Service) MoveFolder(ctx context.Context, input MoveFolderInput) (MoveResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return MoveResult{}, err
	}
	now := s.clock.Now()
	var result MoveResult
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		/* 移动会批量重写子树物化路径，必须与其它目录写入串行化 */
		if err := s.deps.Folders.LockTree(txCtx, input.UserID); err != nil {
			return err
		}
		folder, err := s.activeFolder(txCtx, input.UserID, input.FolderID)
		if err != nil {
			return err
		}
		var target *entity.Folder
		if input.TargetParentID != "" {
			target, err = s.activeFolder(txCtx, input.UserID, input.TargetParentID)
			if err != nil {
				return err
			}
		}
		if target != nil && service.IsWithinSubtree(folder.Path, target.Path) {
			return errs.New(errs.CodeFolderCycle, "不能移动到自身或其子目录")
		}

		newParentID := parentIDOf(target)
		newDepth := 1
		newPath := service.BuildFolderPath("", folder.ID)
		if target != nil {
			newDepth = target.Depth + 1
			newPath = service.BuildFolderPath(target.Path, folder.ID)
		}

		subtree, err := s.deps.Folders.ListSubtree(txCtx, input.UserID, folder.ID)
		if err != nil {
			return err
		}
		height := 0
		for _, node := range subtree {
			if node.Depth-folder.Depth > height {
				height = node.Depth - folder.Depth
			}
		}
		if newDepth+height > entity.MaxFolderDepth {
			return errs.New(errs.CodeFolderDepthExceeded, "移动后子树超过 5 层")
		}

		duplicated, err := s.deps.Folders.NameExists(txCtx, input.UserID, newParentID, folder.Name, folder.ID)
		if err != nil {
			return err
		}
		if duplicated {
			return errs.New(errs.CodeNameDuplicated, "目标目录下已存在同名目录")
		}

		delta := newDepth - folder.Depth
		/* 前缀参照必须在循环前快照：folder 会在循环内被替换为改写后的节点 */
		oldPath := folder.Path
		moved := 0
		changed := make([]*entity.Folder, 0, len(subtree))
		for _, node := range subtree {
			node.Path = service.RelocatePath(node.Path, oldPath, newPath)
			node.Depth += delta
			if node.ID == folder.ID {
				node.ParentID = newParentID
				node.UpdatedAt = now
				folder = node
			}
			if !node.IsDeleted() {
				moved++
			}
			changed = append(changed, node)
		}
		if err := s.deps.Folders.SaveBatch(txCtx, changed); err != nil {
			return err
		}
		noteCount, err := s.countNotesInTree(txCtx, input.UserID, subtree)
		if err != nil {
			return err
		}
		result = MoveResult{Folder: toView(folder), MovedFolderCount: moved, MovedNoteCount: noteCount}
		return nil
	})
	if err != nil {
		return MoveResult{}, err
	}
	result.Folder.ChildCount, err = s.countChildren(ctx, input.UserID, input.FolderID)
	if err != nil {
		return MoveResult{}, err
	}
	return result, nil
}
