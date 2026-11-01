/* 目录回收站到期清理：供后台定时任务批量彻底删除 purge_at 已到期的目录子树 */
package folder

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * PurgeExpiredFolders 彻底删除 purge_at 已到期的目录子树，返回实际清理数量。
 * 子树内笔记与附属数据随目录级联清理；单条失败只跳过该条并继续。
 * 并发下目录可能已被其他清理者删除，此时按已清理计数。
 * 参考：docs/ARCHITECTURE.md#64-回收站恢复与到期清理、PRD FR-09
 */
func (s *Service) PurgeExpiredFolders(ctx context.Context, limit int) (int, error) {
	expired, err := s.deps.Folders.ListExpired(ctx, s.clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, folder := range expired {
		if err := s.ExpungeFolder(ctx, folder.UserID, folder.ID); err != nil {
			if errs.IsCode(err, errs.CodeResourceNotFound) ||
				errs.IsCode(err, errs.CodePreconditionFailed) {
				continue
			}
			return purged, err
		}
		purged++
	}
	return purged, nil
}
