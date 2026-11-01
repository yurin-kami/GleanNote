/*
 * 版本与分享链接的持久化端口。
 *
 * 职责：以领域实体为边界描述版本快照与分享状态的读写，隐藏索引与清理实现。
 * 不负责：来源校验、保留策略与公开可见性判断（由用例负责）。
 * 约束：所有方法必须携带 userID，跨用户访问一律按 RESOURCE_NOT_FOUND 处理（G-08）。
 * 参考：docs/API.md#58-版本、docs/API.md#59-分享
 */
package port

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/*
 * NoteVersionRepository 管理正文版本快照。
 * 约定：ListByNote 按生成时间倒序返回；Prune 删除超出保留数量与保留期的版本，返回删除条数。
 */
type NoteVersionRepository interface {
	Create(ctx context.Context, version *entity.NoteVersion) error
	GetByID(ctx context.Context, userID, noteID, versionID string) (*entity.NoteVersion, error)
	ListByNote(ctx context.Context, userID, noteID string) ([]*entity.NoteVersion, error)
	DeleteByNote(ctx context.Context, userID, noteID string) error
	Prune(ctx context.Context, userID, noteID string, keep int, cutoff time.Time) (int, error)
}

/*
 * ShareLinkRepository 管理笔记分享状态。
 * 约定：GetByNote 在从未开启过分享时返回 RESOURCE_NOT_FOUND，由用例映射为 enabled=false；
 * GetByToken 只按令牌索引查询，供匿名公开读取使用。
 */
type ShareLinkRepository interface {
	GetByNote(ctx context.Context, userID, noteID string) (*entity.ShareLink, error)
	GetByToken(ctx context.Context, token string) (*entity.ShareLink, error)
	Save(ctx context.Context, link *entity.ShareLink) error
	Delete(ctx context.Context, userID, noteID string) error
	/* DeleteByNote 供笔记彻底删除时清理分享记录，语义与 Delete 一致 */
	DeleteByNote(ctx context.Context, userID, noteID string) error
	EnabledByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]bool, error)
}
