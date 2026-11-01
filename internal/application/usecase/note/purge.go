/* 笔记回收站到期清理：供后台定时任务批量彻底删除 purge_at 已到期的笔记 */
package note

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * PurgeExpiredNotes 彻底删除 purge_at 已到期的笔记，返回实际清理数量。
 * 单条失败只跳过该条并继续，避免一条异常阻塞整批清理；
 * 并发下笔记可能已被其他清理者删除，此时按已清理计数。
 * 参考：docs/ARCHITECTURE.md#64-回收站恢复与到期清理、PRD FR-09
 */
func (s *Service) PurgeExpiredNotes(ctx context.Context, limit int) (int, error) {
	expired, err := s.deps.Notes.ListExpired(ctx, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, note := range expired {
		if err := s.ExpungeNote(ctx, note.UserID, note.ID); err != nil {
			if errs.IsCode(err, errs.CodeResourceNotFound) ||
				errs.IsCode(err, errs.CodePreconditionFailed) {
				/* 已被并发恢复或删除，跳过即可 */
				continue
			}
			return purged, err
		}
		purged++
	}
	return purged, nil
}
