/* 笔记回收站用例：软删除、恢复到原目录或根级、彻底删除 */
package note

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * DeleteNote 把笔记移入回收站。
 * 幂等：已删除笔记直接返回首次删除的批次与到期时间，不生成新批次。
 * 参考：docs/API.md#537-delete-v1notesnoteid、PRD FR-09
 */
func (s *Service) DeleteNote(ctx context.Context, userID, noteID string) (DeleteNoteResult, error) {
	if err := requireUser(userID); err != nil {
		return DeleteNoteResult{}, err
	}
	now := s.clock.Now()
	var result DeleteNoteResult
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		note, err := s.deps.Notes.GetForUpdate(txCtx, userID, noteID)
		if err != nil {
			return err
		}
		/* 删除即失效分享链接，恢复后不会重新公开（ARCHITECTURE 5.4、PRD FR-15） */
		if err := s.deps.Shares.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		if note.IsDeleted() {
			purgeAt := now.Add(NoteTrashRetention)
			if note.PurgeAt != nil {
				purgeAt = *note.PurgeAt
			}
			result = DeleteNoteResult{DeleteBatchID: note.DeleteBatchID, PurgeAt: purgeAt}
			return nil
		}
		batchID, err := s.newID("删除批次")
		if err != nil {
			return err
		}
		note.MarkDeleted(batchID, now, NoteTrashRetention)
		if err := s.deps.Notes.Update(txCtx, note); err != nil {
			return err
		}
		result = DeleteNoteResult{DeleteBatchID: batchID, PurgeAt: now.Add(NoteTrashRetention)}
		return nil
	})
	if err != nil {
		return DeleteNoteResult{}, err
	}
	return result, nil
}

/*
 * RestoreNote 恢复回收站中的笔记。
 * 目录裁决在笔记事务之外完成（目录恢复自带事务）：原目录被彻底删除时回落根级，
 * 原目录仍在回收站时先恢复该目录及其必要祖先。
 * 参考：docs/API.md#539-恢复与彻底删除、docs/ARCHITECTURE.md#64-回收站恢复与到期清理
 */
func (s *Service) RestoreNote(ctx context.Context, userID, noteID string) (RestoreNoteResult, error) {
	if err := requireUser(userID); err != nil {
		return RestoreNoteResult{}, err
	}
	current, err := s.deps.Notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return RestoreNoteResult{}, err
	}
	if !current.IsDeleted() {
		return RestoreNoteResult{}, errs.New(errs.CodePreconditionFailed, "笔记不在回收站")
	}
	target, restoredToRoot, err := s.resolveRestoreFolder(ctx, userID, current.FolderID)
	if err != nil {
		return RestoreNoteResult{}, err
	}

	now := s.clock.Now()
	var recovered *entity.Note
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		note, err := s.deps.Notes.GetForUpdate(txCtx, userID, noteID)
		if err != nil {
			return err
		}
		if !note.IsDeleted() {
			return errs.New(errs.CodePreconditionFailed, "笔记不在回收站")
		}
		note.Restore(target, now)
		if err := s.deps.Notes.Update(txCtx, note); err != nil {
			return err
		}
		/* 恢复不回滚分享状态：历史链接即使存在也保持失效 */
		if err := s.deps.Shares.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		recovered = note
		return nil
	})
	if err != nil {
		return RestoreNoteResult{}, err
	}
	view, err := s.buildView(ctx, userID, recovered)
	if err != nil {
		return RestoreNoteResult{}, err
	}
	return RestoreNoteResult{Note: view, RestoredToRoot: restoredToRoot}, nil
}

/*
 * ExpungeNote 彻底删除回收站中的笔记，并释放其原图占用的云端容量。
 * 顺序：事务内删除页面与任务、按原图字节释放容量、删除笔记；提交后再清理对象。
 * 对象删除失败不回滚业务数据，由对象存储生命周期规则兜底（架构 8.8、8.10）。
 * 参考：docs/API.md#539-恢复与彻底删除
 */
func (s *Service) ExpungeNote(ctx context.Context, userID, noteID string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	objectKeys := make([]string, 0, 8)
	var released int64
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		note, err := s.deps.Notes.GetForUpdate(txCtx, userID, noteID)
		if err != nil {
			return err
		}
		if !note.IsDeleted() {
			return errs.New(errs.CodePreconditionFailed, "只能彻底删除回收站中的笔记")
		}
		pages, err := s.deps.Pages.DeleteByNote(txCtx, userID, noteID)
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
		if err := s.deps.Jobs.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		if err := s.deps.TagLinks.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		if err := s.deps.Versions.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		if err := s.deps.Shares.DeleteByNote(txCtx, userID, noteID); err != nil {
			return err
		}
		if released > 0 {
			if err := s.deps.Storage.Release(txCtx, userID, released); err != nil {
				return err
			}
		}
		return s.deps.Notes.Delete(txCtx, userID, noteID)
	})
	if err != nil {
		return err
	}
	for _, key := range objectKeys {
		/* 事务已提交，对象清理失败只留孤儿对象，不影响用户可见状态 */
		_ = s.deps.Objects.Delete(ctx, key)
	}
	return nil
}

/* resolveRestoreFolder 决定恢复后的目录：原目录可用则原地恢复，否则回落根级 */
func (s *Service) resolveRestoreFolder(ctx context.Context, userID, folderID string) (string, bool, error) {
	folder, err := s.deps.Folders.GetByID(ctx, userID, folderID)
	if err != nil {
		if !errs.IsCode(err, errs.CodeResourceNotFound) {
			return "", false, err
		}
		root, err := s.deps.Folders.Root(ctx, userID)
		if err != nil {
			return "", false, err
		}
		return root.ID, true, nil
	}
	if !folder.IsDeleted() {
		return folder.ID, false, nil
	}
	restored, err := s.deps.FolderRestorer.RestoreNoteFolder(ctx, userID, folder.ID)
	if err != nil {
		return "", false, err
	}
	return restored.FolderID, restored.RestoredToRoot, nil
}
