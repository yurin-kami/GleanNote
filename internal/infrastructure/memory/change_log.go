/* 内存变更日志：账号内严格单调的序号、墓碑事件与 90 天保留期 */
package memory

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * ChangeLogRepository 是 port.ChangeLog 的内存实现。
 * 序号按账号独立递增，避免跨账号游标互相影响；清理记录已丢弃的最大序号用于过期判定。
 */
type ChangeLogRepository struct {
	store   *Store
	byUser  map[string][]*entity.ChangeEvent
	nextSeq map[string]int64
	trimmed map[string]int64
}

/* NewChangeLogRepository 初始化空的变更日志 */
func NewChangeLogRepository(store *Store) *ChangeLogRepository {
	return &ChangeLogRepository{
		store:   store,
		byUser:  make(map[string][]*entity.ChangeEvent),
		nextSeq: make(map[string]int64),
		trimmed: make(map[string]int64),
	}
}

/*
 * Append 追加事件并分配序号。
 * 约束：资源类型与操作必须在枚举内；DELETE 强制清空载荷，防止墓碑泄露内容。
 */
func (r *ChangeLogRepository) Append(ctx context.Context, event *entity.ChangeEvent) error {
	release := r.store.lock(ctx)
	defer release()

	return r.appendLocked(ctx, event)
}

/* appendLocked 在已持锁（或事务内）时追加事件，供各仓储写入路径复用 */
func (r *ChangeLogRepository) appendLocked(ctx context.Context, event *entity.ChangeEvent) error {
	if event == nil {
		return errs.New(errs.CodeInternal, "变更事件不能为空")
	}
	if event.UserID == "" || event.ResourceID == "" {
		return errs.New(errs.CodeInternal, "变更事件缺少归属或资源 ID")
	}
	if !entity.ValidChangeResourceType(event.ResourceType) {
		return errs.New(errs.CodeInternal, "未知变更资源类型")
	}
	switch event.Op {
	case entity.ChangeOpCreate, entity.ChangeOpUpdate, entity.ChangeOpDelete:
	default:
		return errs.New(errs.CodeInternal, "未知变更操作")
	}

	stored := cloneChangeEvent(event)
	stored.Seq = r.nextSeq[event.UserID] + 1
	if stored.IsTombstone() {
		stored.Payload = nil
	}
	r.nextSeq[event.UserID] = stored.Seq
	r.byUser[event.UserID] = append(r.byUser[event.UserID], stored)
	r.store.onRollback(ctx, func() {
		events := r.byUser[event.UserID]
		if len(events) > 0 && events[len(events)-1].Seq == stored.Seq {
			r.byUser[event.UserID] = events[:len(events)-1]
		}
		r.nextSeq[event.UserID] = stored.Seq - 1
	})
	event.Seq = stored.Seq
	return nil
}

/* List 返回游标之后的事件；types 为空表示全部类型 */
func (r *ChangeLogRepository) List(ctx context.Context, userID string, since int64, limit int,
	types []entity.ChangeResourceType) ([]*entity.ChangeEvent, bool, error) {
	release := r.store.lock(ctx)
	defer release()

	wanted := make(map[entity.ChangeResourceType]struct{}, len(types))
	for _, item := range types {
		wanted[item] = struct{}{}
	}
	filtered := make([]*entity.ChangeEvent, 0, limit+1)
	for _, event := range r.byUser[userID] {
		if event.Seq <= since {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[event.ResourceType]; !ok {
				continue
			}
		}
		filtered = append(filtered, event)
		if limit > 0 && len(filtered) > limit {
			break
		}
	}
	hasMore := limit > 0 && len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	return cloneChangeEvents(filtered), hasMore, nil
}

/* TrimmedThrough 返回已清理的最大序号 */
func (r *ChangeLogRepository) TrimmedThrough(ctx context.Context, userID string) (int64, error) {
	release := r.store.lock(ctx)
	defer release()

	return r.trimmed[userID], nil
}

/*
 * Prune 清理早于 before 的事件，并记录已丢弃的最大序号。
 * 事务内回滚时恢复被删事件与清理水位。
 */
func (r *ChangeLogRepository) Prune(ctx context.Context, before time.Time) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	removed := 0
	for userID, events := range r.byUser {
		kept := make([]*entity.ChangeEvent, 0, len(events))
		maxSeq := r.trimmed[userID]
		for _, event := range events {
			if event.UpdatedAt.Before(before) {
				removed++
				if event.Seq > maxSeq {
					maxSeq = event.Seq
				}
				continue
			}
			kept = append(kept, event)
		}
		if len(kept) == len(events) {
			continue
		}
		previousEvents := r.byUser[userID]
		previousTrimmed := r.trimmed[userID]
		r.byUser[userID] = kept
		r.trimmed[userID] = maxSeq
		user := userID
		r.store.onRollback(ctx, func() {
			r.byUser[user] = previousEvents
			r.trimmed[user] = previousTrimmed
		})
	}
	return removed, nil
}

/* cloneChangeEvents 深拷贝切片，避免调用方修改仓储内对象 */
func cloneChangeEvents(events []*entity.ChangeEvent) []*entity.ChangeEvent {
	if len(events) == 0 {
		return nil
	}
	copied := make([]*entity.ChangeEvent, len(events))
	for index, event := range events {
		copied[index] = cloneChangeEvent(event)
	}
	return copied
}

/* cloneChangeEvent 深拷贝事件与载荷 */
func cloneChangeEvent(event *entity.ChangeEvent) *entity.ChangeEvent {
	if event == nil {
		return nil
	}
	copied := *event
	if len(event.Payload) > 0 {
		payload := make(map[string]any, len(event.Payload))
		for key, value := range event.Payload {
			payload[key] = value
		}
		copied.Payload = payload
	}
	return &copied
}

/* 确保实现端口契约 */
var _ port.ChangeLog = (*ChangeLogRepository)(nil)
