/* 目录回收站用例：整棵子树软删除、按批次恢复与彻底删除 */
package folder

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * DeleteFolder 软删除整棵子树，并为本次删除生成一个批次。
 * 已删除的子节点保留原有批次，不会并入新批次（等价于保持原回收站记录）。
 * 参考：docs/API.md#527-delete-v1foldersfolderid、PRD FR-09
 */
func (s *Service) DeleteFolder(ctx context.Context, userID, folderID string) (DeleteResult, error) {
	if err := requireUser(userID); err != nil {
		return DeleteResult{}, err
	}
	now := s.clock.Now()
	var result DeleteResult
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.Folders.LockTree(txCtx, userID); err != nil {
			return err
		}
		if _, err := s.activeFolder(txCtx, userID, folderID); err != nil {
			return err
		}
		batchID, err := s.newID()
		if err != nil {
			return err
		}
		subtree, err := s.deps.Folders.ListSubtree(txCtx, userID, folderID)
		if err != nil {
			return err
		}
		changed := make([]*entity.Folder, 0, len(subtree))
		for _, node := range subtree {
			if node.IsDeleted() {
				continue
			}
			node.MarkDeleted(batchID, now, TrashRetention)
			changed = append(changed, node)
		}
		if err := s.deps.Folders.SaveBatch(txCtx, changed); err != nil {
			return err
		}
		/* 子树内笔记并入同一删除批次，回收站按批次整体恢复 */
		noteCount, err := s.markNotesDeleted(txCtx, userID, subtree, batchID, now)
		if err != nil {
			return err
		}
		/* 目录删除同样失效分享链接，避免恢复目录后旧 token 重新公开（PRD FR-15） */
		if err := s.invalidateShares(txCtx, userID, subtree); err != nil {
			return err
		}
		result = DeleteResult{
			DeleteBatchID: batchID,
			FolderCount:   len(changed),
			NoteCount:     noteCount,
			PurgeAt:       now.Add(TrashRetention),
		}
		return nil
	})
	if err != nil {
		return DeleteResult{}, err
	}
	return result, nil
}

/*
 * RestoreFolder 恢复整棵子树，仅恢复与根节点同批次的节点。
 * 原父级不可见时整棵恢复到根级并返回 RestoredToRoot；同级重名追加「 (2)」。
 * 参考：docs/API.md#528-恢复与彻底删除、docs/ARCHITECTURE.md#812-安全与隐私
 */
func (s *Service) RestoreFolder(ctx context.Context, userID, folderID string) (FolderView, error) {
	if err := requireUser(userID); err != nil {
		return FolderView{}, err
	}
	now := s.clock.Now()
	var view FolderView
	restoredToRoot := false
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.Folders.LockTree(txCtx, userID); err != nil {
			return err
		}
		folder, err := s.deps.Folders.GetByID(txCtx, userID, folderID)
		if err != nil {
			return err
		}
		if !folder.IsDeleted() {
			return errs.New(errs.CodePreconditionFailed, "目录不在回收站")
		}

		parent, keepParent, err := s.restoreParent(txCtx, userID, folder)
		if err != nil {
			return err
		}
		restoredToRoot = !keepParent
		newParentID := parentIDOf(parent)
		newDepth := 1
		newPath := service.BuildFolderPath("", folder.ID)
		if parent != nil {
			newDepth = parent.Depth + 1
			newPath = service.BuildFolderPath(parent.Path, folder.ID)
		}

		chosen, renamed, err := s.uniqueRestoredName(txCtx, userID, newParentID,
			valueobject.FolderName(folder.Name), folder.ID)
		if err != nil {
			return err
		}
		subtree, err := s.deps.Folders.ListSubtree(txCtx, userID, folder.ID)
		if err != nil {
			return err
		}
		delta := newDepth - folder.Depth
		/* 批次与旧路径必须在循环前快照：folder 会被替换为恢复后的节点 */
		batchID := folder.DeleteBatchID
		oldPath := folder.Path
		changed := make([]*entity.Folder, 0, len(subtree))
		for _, node := range subtree {
			if node.DeletedAt == nil || node.DeleteBatchID != batchID {
				continue
			}
			node.Path = service.RelocatePath(node.Path, oldPath, newPath)
			node.Depth += delta
			if node.ID == folder.ID {
				node.Restore(newParentID, newDepth, newPath, now)
				node.Name = chosen.String()
				if renamed {
					node.Revision++
				}
				folder = node
			} else {
				node.Restore(node.ParentID, node.Depth, node.Path, now)
			}
			changed = append(changed, node)
		}
		if err := s.deps.Folders.SaveBatch(txCtx, changed); err != nil {
			return err
		}
		/* 同一删除批次内的笔记随目录一并恢复，避免出现悬空笔记 */
		if _, err := s.deps.Notes.RestoreByBatch(txCtx, userID, batchID, now); err != nil {
			return err
		}
		/* 恢复不回滚分享状态：历史链接即使存在也保持失效 */
		if err := s.invalidateShares(txCtx, userID, subtree); err != nil {
			return err
		}
		view = toView(folder)
		return nil
	})
	if err != nil {
		return FolderView{}, err
	}
	view.RestoredToRoot = restoredToRoot
	view.ChildCount, err = s.countChildren(ctx, userID, folderID)
	if err != nil {
		return FolderView{}, err
	}
	return view, nil
}

/*
 * ExpungeFolder 彻底删除同批次的子树，并级联清理子树内笔记及其附属数据。
 * 顺序：事务内删除页面/任务/版本/标签/分享 → 释放原图容量 → 删除笔记与目录；
 * 提交后再清理对象存储，失败只留孤儿对象，由生命周期规则兜底（架构 8.8、8.10）。
 * 参考：docs/API.md#528-恢复与彻底删除
 */
func (s *Service) ExpungeFolder(ctx context.Context, userID, folderID string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	objectKeys := make([]string, 0, 8)
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.Folders.LockTree(txCtx, userID); err != nil {
			return err
		}
		folder, err := s.deps.Folders.GetByID(txCtx, userID, folderID)
		if err != nil {
			return err
		}
		if !folder.IsDeleted() {
			return errs.New(errs.CodePreconditionFailed, "只能彻底删除回收站中的目录")
		}
		subtree, err := s.deps.Folders.ListSubtree(txCtx, userID, folderID)
		if err != nil {
			return err
		}
		notes, err := s.deps.Notes.ListByFolders(txCtx, userID, folderIDsOf(subtree))
		if err != nil {
			return err
		}
		var released int64
		for _, note := range notes {
			pages, err := s.deps.Pages.DeleteByNote(txCtx, userID, note.ID)
			if err != nil {
				return err
			}
			for _, page := range pages {
				released += page.Bytes
				if page.ObjectKey != "" {
					objectKeys = append(objectKeys, page.ObjectKey)
				}
				if page.ThumbKey != "" {
					objectKeys = append(objectKeys, page.ThumbKey)
				}
			}
			if err := s.deps.Jobs.DeleteByNote(txCtx, userID, note.ID); err != nil {
				return err
			}
			if err := s.deps.TagLinks.DeleteByNote(txCtx, userID, note.ID); err != nil {
				return err
			}
			if err := s.deps.Versions.DeleteByNote(txCtx, userID, note.ID); err != nil {
				return err
			}
			if err := s.deps.Shares.DeleteByNote(txCtx, userID, note.ID); err != nil {
				return err
			}
			if err := s.deps.Notes.Delete(txCtx, userID, note.ID); err != nil {
				return err
			}
		}
		if released > 0 {
			if err := s.deps.Storage.Release(txCtx, userID, released); err != nil {
				return err
			}
		}
		_, err = s.deps.Folders.DeleteBatch(txCtx, userID, folderID)
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range objectKeys {
		_ = s.deps.Objects.Delete(ctx, key)
	}
	return nil
}

/* invalidateShares 失效子树内全部笔记的分享链接，供目录删除与恢复复用 */
func (s *Service) invalidateShares(ctx context.Context, userID string, nodes []*entity.Folder) error {
	notes, err := s.deps.Notes.ListByFolders(ctx, userID, folderIDsOf(nodes))
	if err != nil {
		return err
	}
	for _, note := range notes {
		if err := s.deps.Shares.DeleteByNote(ctx, userID, note.ID); err != nil {
			return err
		}
	}
	return nil
}

/* markNotesDeleted 把子树内未删除笔记并入目录删除批次，返回受影响数量 */
func (s *Service) markNotesDeleted(ctx context.Context, userID string, nodes []*entity.Folder,
	batchID string, now time.Time) (int, error) {
	ids := folderIDsOf(nodes)
	if len(ids) == 0 {
		return 0, nil
	}
	return s.deps.Notes.MarkDeletedByFolders(ctx, userID, ids, batchID, now, TrashRetention)
}

/* restoreParent 解析恢复后的父级；父级缺失或已删除时回落到根级 */
func (s *Service) restoreParent(ctx context.Context, userID string, folder *entity.Folder) (*entity.Folder, bool, error) {
	if folder.ParentID == nil {
		return nil, false, nil
	}
	parent, err := s.deps.Folders.GetByID(ctx, userID, *folder.ParentID)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if parent.IsDeleted() {
		return nil, false, nil
	}
	return parent, true, nil
}

/* uniqueRestoredName 在同级中寻找可用名称，冲突时追加「 (2)」等后缀 */
func (s *Service) uniqueRestoredName(ctx context.Context, userID string, parentID *string,
	name valueobject.FolderName, excludeID string) (valueobject.FolderName, bool, error) {
	for attempt := 0; attempt <= valueobject.MaxFolderNameLength; attempt++ {
		candidate := name
		if attempt > 0 {
			candidate = name.WithDuplicateSuffix(attempt)
		}
		exists, err := s.deps.Folders.NameExists(ctx, userID, parentID, candidate.String(), excludeID)
		if err != nil {
			return "", false, err
		}
		if !exists {
			return candidate, attempt > 0, nil
		}
	}
	return "", false, errs.New(errs.CodeNameDuplicated, "同级目录重名过多")
}
