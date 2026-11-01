/* 版本生成与清理：保存、冲突副本、转换与恢复共用的写入入口 */
package version

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * Record 写入一次正文快照。
 * 自动保存版本按「同一笔记两次间隔不少于 5 分钟」节流（PRD FR-16）；
 * 调用方处于事务中时，快照写入与正文写入同事务提交。
 */
func (s *Service) Record(ctx context.Context, userID, noteID, source, content string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	versionSource := entity.VersionSource(source)
	switch versionSource {
	case entity.VersionSourceManualSave, entity.VersionSourceAutoSave,
		entity.VersionSourceConflictCopy, entity.VersionSourceConversion:
	default:
		return errs.New(errs.CodeValidationFailed, "版本来源不支持")
	}
	now := s.clock.Now()
	if versionSource == entity.VersionSourceAutoSave {
		latest, err := s.deps.Versions.ListByNote(ctx, userID, noteID)
		if err != nil {
			return err
		}
		/* 以最近一条 AUTO_SAVE 为基准：中间的 MANUAL/CONVERSION 不重置 5 分钟节流 */
		for _, item := range latest {
			if item.Source != entity.VersionSourceAutoSave {
				continue
			}
			if now.Sub(item.CreatedAt) < entity.AutoVersionMinInterval {
				return nil
			}
			break
		}
	}
	return s.create(ctx, userID, noteID, versionSource, content, now)
}

/* DeleteByNote 删除笔记全部版本；笔记彻底删除时与正文同事务调用 */
func (s *Service) DeleteByNote(ctx context.Context, userID, noteID string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	return s.deps.Versions.DeleteByNote(ctx, userID, noteID)
}

/* create 落库快照并按「最近 50 个 / 90 天」清理超出保留范围的版本 */
func (s *Service) create(ctx context.Context, userID, noteID string,
	source entity.VersionSource, content string, now time.Time) error {
	id, err := s.newID()
	if err != nil {
		return err
	}
	version := entity.NewNoteVersion(id, userID, noteID, source, valueobject.NoteContent(content), now)
	if err := s.deps.Versions.Create(ctx, version); err != nil {
		return err
	}
	if _, err := s.deps.Versions.Prune(ctx, userID, noteID,
		entity.MaxVersionsPerNote, now.Add(-entity.VersionRetention)); err != nil {
		return err
	}
	return nil
}
