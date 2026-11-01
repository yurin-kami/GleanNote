/* 变更日志仓储：账号内严格单调序号、墓碑事件与保留期清理 */
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* ChangeLogRepository 是 port.ChangeLog 的 PostgreSQL 实现 */
type ChangeLogRepository struct {
	db *DB
}

/* NewChangeLogRepository 绑定连接池 */
func NewChangeLogRepository(db *DB) *ChangeLogRepository {
	return &ChangeLogRepository{db: db}
}

/*
 * Append 追加事件并分配账号内递增序号。
 * 约束：DELETE 强制清空载荷，防止墓碑事件泄露正文。
 */
func (r *ChangeLogRepository) Append(ctx context.Context, event *entity.ChangeEvent) error {
	if _, err := r.appendEvent(ctx, event); err != nil {
		return err
	}
	return nil
}

/* appendEvent 供各仓储在写事务内复用，返回分配到的序号 */
func (r *ChangeLogRepository) appendEvent(ctx context.Context, event *entity.ChangeEvent) (int64, error) {
	if event == nil {
		return 0, errs.New(errs.CodeInternal, "变更事件不能为空")
	}
	if event.UserID == "" || event.ResourceID == "" {
		return 0, errs.New(errs.CodeInternal, "变更事件缺少归属或资源 ID")
	}
	if !entity.ValidChangeResourceType(event.ResourceType) {
		return 0, errs.New(errs.CodeInternal, "未知变更资源类型")
	}
	switch event.Op {
	case entity.ChangeOpCreate, entity.ChangeOpUpdate, entity.ChangeOpDelete:
	default:
		return 0, errs.New(errs.CodeInternal, "未知变更操作")
	}

	seq, err := r.nextSeq(ctx, event.UserID)
	if err != nil {
		return 0, err
	}
	var payload any
	if !event.IsTombstone() && len(event.Payload) > 0 {
		encoded, err := json.Marshal(event.Payload)
		if err != nil {
			return 0, fmt.Errorf("编码变更载荷: %w", err)
		}
		payload = string(encoded)
	}
	_, err = r.db.q(ctx).Exec(ctx, `
		INSERT INTO change_events (user_id, seq, resource_type, resource_id, op, revision, updated_at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
		event.UserID, seq, string(event.ResourceType), event.ResourceID, string(event.Op),
		event.Revision, event.UpdatedAt, payload)
	if err != nil {
		return 0, fmt.Errorf("写入变更事件: %w", err)
	}
	event.Seq = seq
	return seq, nil
}

/* nextSeq 通过计数表的自增保证并发下序号不重复 */
func (r *ChangeLogRepository) nextSeq(ctx context.Context, userID string) (int64, error) {
	var seq int64
	err := r.db.q(ctx).QueryRow(ctx, `
		INSERT INTO change_seq (user_id, next_seq) VALUES ($1, 1)
		ON CONFLICT (user_id) DO UPDATE SET next_seq = change_seq.next_seq + 1
		RETURNING next_seq`, userID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("分配变更序号: %w", err)
	}
	return seq, nil
}

/* List 返回游标之后的事件；types 为空表示全部类型 */
func (r *ChangeLogRepository) List(ctx context.Context, userID string, since int64, limit int,
	types []entity.ChangeResourceType) ([]*entity.ChangeEvent, bool, error) {
	query := `
		SELECT seq, resource_type, resource_id, op, revision, updated_at, payload
		FROM change_events WHERE user_id = $1 AND seq > $2`
	args := []any{userID, since}
	if len(types) > 0 {
		query += ` AND resource_type = ANY($3)`
		args = append(args, resourceStrings(types))
	}
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(` ORDER BY seq ASC LIMIT %d`, limit+1)

	rows, err := r.db.q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("查询变更事件: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.ChangeEvent, 0, limit+1)
	for rows.Next() {
		event := &entity.ChangeEvent{UserID: userID}
		var resourceType, op string
		var payload []byte
		if err := rows.Scan(&event.Seq, &resourceType, &event.ResourceID, &op,
			&event.Revision, &event.UpdatedAt, &payload); err != nil {
			return nil, false, fmt.Errorf("扫描变更事件: %w", err)
		}
		event.ResourceType = entity.ChangeResourceType(resourceType)
		event.Op = entity.ChangeOperation(op)
		if len(payload) > 0 {
			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				return nil, false, fmt.Errorf("解析变更载荷: %w", err)
			}
			event.Payload = decoded
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("遍历变更事件: %w", err)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

/* TrimmedThrough 返回该账号已清理的最大序号；未清理过为 0 */
func (r *ChangeLogRepository) TrimmedThrough(ctx context.Context, userID string) (int64, error) {
	var trimmed int64
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT trimmed_through FROM change_watermarks WHERE user_id = $1`, userID).Scan(&trimmed)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("查询同步水位: %w", err)
	}
	return trimmed, nil
}

/*
 * Prune 清理 before 之前的全部账号事件，并推进各账号的已清理水位。
 * 数据修改型 CTE 无论是否被引用都会执行，因此单条语句即可完成删除与水位更新。
 */
func (r *ChangeLogRepository) Prune(ctx context.Context, before time.Time) (int, error) {
	var removed int
	err := r.db.q(ctx).QueryRow(ctx, `
		WITH removed AS (
			DELETE FROM change_events WHERE updated_at < $1 RETURNING user_id, seq
		), agg AS (
			SELECT user_id, MAX(seq) AS max_seq, COUNT(*) AS count FROM removed GROUP BY user_id
		), bumped AS (
			INSERT INTO change_watermarks (user_id, trimmed_through)
			SELECT user_id, max_seq FROM agg
			ON CONFLICT (user_id) DO UPDATE
				SET trimmed_through = GREATEST(change_watermarks.trimmed_through, EXCLUDED.trimmed_through)
			RETURNING 1
		)
		SELECT COALESCE(SUM(count), 0) FROM agg`, before).Scan(&removed)
	if err != nil {
		return 0, fmt.Errorf("清理变更日志: %w", err)
	}
	return removed, nil
}

/* resourceStrings 把资源类型枚举转为 SQL 数组可用的字符串切片 */
func resourceStrings(types []entity.ChangeResourceType) []string {
	values := make([]string, 0, len(types))
	for _, item := range types {
		values = append(values, string(item))
	}
	return values
}

/* 确保实现端口契约 */
var _ port.ChangeLog = (*ChangeLogRepository)(nil)
