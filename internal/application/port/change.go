/*
 * 增量同步的持久化端口。
 *
 * 职责：以变更事件为边界描述追加、按游标读取与保留期清理。
 * 不负责：游标过期判定与资源过滤白名单（由用例负责）。
 * 约束：Append 必须与业务写入处于同一事务上下文；保留期 90 天（docs/API.md#510-增量同步）。
 */
package port

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* ChangeLog 管理账号内严格单调的变更日志 */
type ChangeLog interface {
	Append(ctx context.Context, event *entity.ChangeEvent) error
	/* List 返回 seq 严格大于 since 的事件，hasMore 表示仍有下一页 */
	List(ctx context.Context, userID string, since int64, limit int,
		types []entity.ChangeResourceType) (items []*entity.ChangeEvent, hasMore bool, err error)
	/* TrimmedThrough 返回该账号已清理的最大序号；since 小于它表示游标超出可追溯范围 */
	TrimmedThrough(ctx context.Context, userID string) (int64, error)
	/* Prune 清理 before 之前的事件并返回条数 */
	Prune(ctx context.Context, before time.Time) (int, error)
}
