/* 版本恢复用例：以历史正文替换当前正文，并生成新的 RESTORE 版本 */
package version

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * RestoreVersion 把笔记正文替换为指定版本的正文。
 * 规则：只替换正文，标题、标签与目录保持不变；不删除任何历史版本，
 * 并写入一条 source=RESTORE 的新版本（PRD FR-16）。
 * 返回笔记 ID，由传输层读取最新详情。
 * 参考：docs/API.md#58-版本
 */
func (s *Service) RestoreVersion(ctx context.Context, userID, noteID, versionID string) (string, error) {
	if err := requireUser(userID); err != nil {
		return "", err
	}
	if versionID == "" {
		return "", errs.New(errs.CodeValidationFailed, "versionId 必填")
	}
	now := s.clock.Now()
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		version, err := s.deps.Versions.GetByID(txCtx, userID, noteID, versionID)
		if err != nil {
			return err
		}
		if version.Expired(now) {
			return errs.New(errs.CodeResourceNotFound, "版本已过期")
		}
		note, err := s.deps.Notes.GetByID(txCtx, userID, noteID)
		if err != nil {
			return err
		}
		if err := note.Editable(); err != nil {
			return err
		}
		if err := note.Save(valueobject.NoteTitle(note.Title), valueobject.NoteContent(version.Content), now); err != nil {
			return err
		}
		if err := s.deps.Notes.Update(txCtx, note); err != nil {
			return err
		}
		return s.create(txCtx, userID, noteID, entity.VersionSourceRestore, note.Content, now)
	})
	if err != nil {
		return "", err
	}
	return noteID, nil
}
