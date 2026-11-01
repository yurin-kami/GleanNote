/* 笔记仓储的 PostgreSQL 实现：目录分页、全文子串搜索与批量回收站操作 */
package postgres

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* NoteRepository 是 port.NoteRepository 的 PostgreSQL 实现 */
type NoteRepository struct {
	db      *DB
	changes *ChangeLogRepository
}

/* NewNoteRepository 绑定连接池与变更日志 */
func NewNoteRepository(db *DB, changes *ChangeLogRepository) *NoteRepository {
	return &NoteRepository{db: db, changes: changes}
}

/* noteColumns 是读取笔记的列清单 */
const noteColumns = `id, user_id, folder_id, title, content, state, revision, char_count,
	conflict_copy, deleted_at, delete_batch_id, purge_at, created_at, updated_at`

/* Create 插入笔记并追加同步事件 */
func (r *NoteRepository) Create(ctx context.Context, note *entity.Note) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO notes (`+noteColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		note.ID, note.UserID, note.FolderID, note.Title, note.Content, string(note.State),
		note.Revision, note.CharCount, note.ConflictCopy, note.DeletedAt,
		note.DeleteBatchID, note.PurgeAt, note.CreatedAt, note.UpdatedAt)
	if err != nil {
		return fmt.Errorf("创建笔记: %w", err)
	}
	return r.appendEvent(ctx, noteChangeEvent(note, entity.ChangeOpCreate, note.UpdatedAt))
}

/* GetByID 返回笔记原始行（含已删除笔记） */
func (r *NoteRepository) GetByID(ctx context.Context, userID, noteID string) (*entity.Note, error) {
	note, err := scanNote(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+noteColumns+` FROM notes WHERE user_id = $1 AND id = $2`, userID, noteID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "笔记不存在")
	}
	return note, nil
}

/* List 返回目录内笔记；State 为空时排除回收站笔记 */
func (r *NoteRepository) List(ctx context.Context, query port.NoteListQuery) ([]*entity.Note, int, error) {
	base := `SELECT ` + noteColumns + ` FROM notes WHERE user_id = $1 AND folder_id = $2`
	args := []any{query.UserID, query.FolderID}
	if query.State == nil {
		base += ` AND deleted_at IS NULL`
	} else {
		base += ` AND state = $3`
		args = append(args, string(*query.State))
	}
	if query.UpdatedAfter != nil {
		base += fmt.Sprintf(` AND updated_at > $%d`, len(args)+1)
		args = append(args, *query.UpdatedAfter)
	}

	var total int
	if err := r.db.q(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM (`+base+`) AS filtered`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计笔记: %w", err)
	}

	statement := base
	if cursor := query.After; cursor != nil {
		switch query.Order {
		case port.NoteOrderTitleAsc:
			statement += fmt.Sprintf(` AND (lower(title) > $%d OR (lower(title) = $%d AND id > $%d))`,
				len(args)+1, len(args)+1, len(args)+2)
			args = append(args, cursor.SortKey, cursor.ID)
		default:
			column := "updated_at"
			if query.Order == port.NoteOrderCreatedDesc {
				column = "created_at"
			}
			statement += fmt.Sprintf(` AND (%s < $%d::timestamptz OR (%s = $%d::timestamptz AND id > $%d))`,
				column, len(args)+1, column, len(args)+1, len(args)+2)
			args = append(args, cursorTimeFromKey(cursor.SortKey), cursor.ID)
		}
	}
	switch query.Order {
	case port.NoteOrderCreatedDesc:
		statement += ` ORDER BY created_at DESC, id ASC`
	case port.NoteOrderTitleAsc:
		statement += ` ORDER BY lower(title) ASC, id ASC`
	default:
		statement += ` ORDER BY updated_at DESC, id ASC`
	}
	if query.Limit > 0 {
		statement += fmt.Sprintf(` LIMIT %d`, query.Limit)
	}

	notes, err := r.queryNotes(ctx, statement, args...)
	if err != nil {
		return nil, 0, err
	}
	return notes, total, nil
}

/*
 * Search 在未删除笔记中按标题与正文做大小写不敏感的子串匹配。
 * 排序：标题命中优先，同级按更新时间倒序；totalSize 为全部命中数。
 */
func (r *NoteRepository) Search(ctx context.Context, query port.NoteSearchQuery) ([]*entity.Note, int, error) {
	inner := `SELECT ` + noteColumns + `,
			CASE WHEN strpos(lower(title), lower($2)) > 0 THEN 0 ELSE 1 END AS tier
		FROM notes
		WHERE user_id = $1 AND deleted_at IS NULL
		AND (strpos(lower(title), lower($2)) > 0 OR strpos(lower(content), lower($2)) > 0)`
	args := []any{query.UserID, query.Keyword}
	if query.FolderIDs != nil {
		inner += fmt.Sprintf(` AND folder_id = ANY($%d)`, len(args)+1)
		args = append(args, query.FolderIDs)
	}
	if query.NoteIDs != nil {
		inner += fmt.Sprintf(` AND id = ANY($%d)`, len(args)+1)
		args = append(args, query.NoteIDs)
	}

	var total int
	if err := r.db.q(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM (`+inner+`) AS matched`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计搜索命中: %w", err)
	}

	statement := `SELECT ` + noteColumns + ` FROM (` + inner + `) AS n`
	if cursor := query.After; cursor != nil {
		tier, nanos, ok := parseSearchKey(cursor.SortKey)
		if ok && tier == "0" {
			statement += fmt.Sprintf(` WHERE (tier = 0 AND (updated_at < $%d::timestamptz
				OR (updated_at = $%d::timestamptz AND id > $%d))) OR tier = 1`,
				len(args)+1, len(args)+1, len(args)+2)
			args = append(args, nanos, cursor.ID)
		} else if ok {
			statement += fmt.Sprintf(` WHERE tier = 1 AND (updated_at < $%d::timestamptz
				OR (updated_at = $%d::timestamptz AND id > $%d))`,
				len(args)+1, len(args)+1, len(args)+2)
			args = append(args, nanos, cursor.ID)
		}
	}
	statement += ` ORDER BY tier ASC, updated_at DESC, id ASC`
	if query.Limit > 0 {
		statement += fmt.Sprintf(` LIMIT %d`, query.Limit)
	}

	notes, err := r.queryNotes(ctx, statement, args...)
	if err != nil {
		return nil, 0, err
	}
	return notes, total, nil
}

/* ListDeleted 返回用户全部回收站笔记，供回收站聚合按批次分组 */
func (r *NoteRepository) ListDeleted(ctx context.Context, userID string) ([]*entity.Note, error) {
	return r.queryNotes(ctx, `SELECT `+noteColumns+` FROM notes
		WHERE user_id = $1 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC, id ASC`, userID)
}

/*
 * GetForUpdate 在事务内以 SELECT ... FOR UPDATE 锁定笔记行。
 * 约束：必须在 WithinTransaction 内调用，否则行锁随语句结束释放，失去并发保护意义。
 */
func (r *NoteRepository) GetForUpdate(ctx context.Context, userID, noteID string) (*entity.Note, error) {
	if _, ok := txFrom(ctx); !ok {
		return nil, errs.New(errs.CodeInternal, "笔记写入必须在事务内执行")
	}
	note, err := scanNote(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+noteColumns+` FROM notes WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, noteID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "笔记不存在")
	}
	return note, nil
}

/* ListByFolders 返回给定目录下的全部笔记（含回收站），供目录级联删除与彻底删除 */
func (r *NoteRepository) ListByFolders(ctx context.Context, userID string, folderIDs []string) ([]*entity.Note, error) {
	if len(folderIDs) == 0 {
		return nil, nil
	}
	return r.queryNotes(ctx, `SELECT `+noteColumns+` FROM notes
		WHERE user_id = $1 AND folder_id = ANY($2)
		ORDER BY created_at ASC, id ASC`, userID, folderIDs)
}

/* ListExpired 返回 purge_at 已到期的回收站笔记，供定时清理任务消费 */
func (r *NoteRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Note, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.queryNotes(ctx, `SELECT `+noteColumns+` FROM notes
		WHERE deleted_at IS NOT NULL AND purge_at IS NOT NULL AND purge_at <= $1
		ORDER BY purge_at ASC, id ASC LIMIT $2`, now, limit)
}

/* Update 覆盖笔记行；依据删除状态判定同步事件类型 */
func (r *NoteRepository) Update(ctx context.Context, note *entity.Note) error {
	_, err := r.write(ctx, note, -1)
	return err
}

/* UpdateWithRevision 以 revision 条件更新笔记行；expected < 0 表示不做乐观锁校验 */
func (r *NoteRepository) UpdateWithRevision(ctx context.Context, note *entity.Note, expected int) (bool, error) {
	return r.write(ctx, note, expected)
}

/*
 * write 是 Update/UpdateWithRevision 的共用实现。
 * expected >= 0 时追加 `AND revision = $expected`，0 行命中即返回 false 且不写同步事件。
 */
func (r *NoteRepository) write(ctx context.Context, note *entity.Note, expected int) (bool, error) {
	var previousDeleted *time.Time
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT deleted_at FROM notes WHERE user_id = $1 AND id = $2`,
		note.UserID, note.ID).Scan(&previousDeleted)
	if err != nil {
		return false, mapNotFound(err, errs.CodeResourceNotFound, "笔记不存在")
	}
	statement := `UPDATE notes SET folder_id = $3, title = $4, content = $5, state = $6, revision = $7,
		char_count = $8, conflict_copy = $9, deleted_at = $10, delete_batch_id = $11,
		purge_at = $12, updated_at = $13
		WHERE user_id = $1 AND id = $2`
	args := []any{note.UserID, note.ID, note.FolderID, note.Title, note.Content, string(note.State),
		note.Revision, note.CharCount, note.ConflictCopy, note.DeletedAt,
		note.DeleteBatchID, note.PurgeAt, note.UpdatedAt}
	if expected >= 0 {
		statement += ` AND revision = $14`
		args = append(args, expected)
	}
	tag, err := r.db.q(ctx).Exec(ctx, statement, args...)
	if err != nil {
		return false, fmt.Errorf("更新笔记: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	op := entity.ChangeOpUpdate
	if previousDeleted == nil && note.IsDeleted() {
		op = entity.ChangeOpDelete
	}
	if err := r.appendEvent(ctx, noteChangeEvent(note, op, note.UpdatedAt)); err != nil {
		return false, err
	}
	return true, nil
}

/* Delete 物理删除单篇笔记并写入墓碑事件 */
func (r *NoteRepository) Delete(ctx context.Context, userID, noteID string) error {
	note, err := r.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}
	if _, err := r.db.q(ctx).Exec(ctx,
		`DELETE FROM notes WHERE user_id = $1 AND id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("删除笔记: %w", err)
	}
	return r.appendEvent(ctx, noteChangeEvent(note, entity.ChangeOpDelete, nowFunc()))
}

/* DeleteAllByUser 清除账号全部笔记；注销用例在一个事务内调用 */
func (r *NoteRepository) DeleteAllByUser(ctx context.Context, userID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM notes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("删除账号笔记: %w", err)
	}
	return nil
}

/* CountActiveByFolder 统计给定目录下未删除笔记数，供目录列表回填 noteCount */
func (r *NoteRepository) CountActiveByFolder(ctx context.Context, userID string, folderIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(folderIDs))
	if len(folderIDs) == 0 {
		return counts, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT folder_id, COUNT(*) FROM notes
		WHERE user_id = $1 AND deleted_at IS NULL AND folder_id = ANY($2)
		GROUP BY folder_id`, userID, folderIDs)
	if err != nil {
		return nil, fmt.Errorf("统计目录笔记: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var folderID string
		var count int
		if err := rows.Scan(&folderID, &count); err != nil {
			return nil, fmt.Errorf("扫描目录笔记计数: %w", err)
		}
		counts[folderID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历目录笔记计数: %w", err)
	}
	return counts, nil
}

/* MarkDeletedByFolders 把目录内未删除笔记并入同一删除批次，返回受影响数量 */
func (r *NoteRepository) MarkDeletedByFolders(ctx context.Context, userID string, folderIDs []string,
	batchID string, now time.Time, retention time.Duration) (int, error) {
	if len(folderIDs) == 0 {
		return 0, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		UPDATE notes SET state = $3, deleted_at = $4, purge_at = $5,
			delete_batch_id = $6, updated_at = $4
		WHERE user_id = $1 AND folder_id = ANY($2) AND deleted_at IS NULL
		RETURNING `+noteColumns,
		userID, folderIDs, string(entity.NoteStateDeleted), now, now.Add(retention), batchID)
	if err != nil {
		return 0, fmt.Errorf("批量移入回收站: %w", err)
	}
	defer rows.Close()

	affected := 0
	pending := make([]*entity.Note, 0)
	for rows.Next() {
		note, err := scanNote(rows)
		if err != nil {
			return 0, fmt.Errorf("扫描已删除笔记: %w", err)
		}
		pending = append(pending, note)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历已删除笔记: %w", err)
	}
	/* 必须先关闭游标再写同步事件：pgx 同一连接不能在读取行时执行新语句 */
	rows.Close()
	for _, note := range pending {
		affected++
		if err := r.appendEvent(ctx, noteChangeEvent(note, entity.ChangeOpDelete, now)); err != nil {
			return 0, err
		}
	}
	return affected, nil
}

/* RestoreByBatch 恢复同一删除批次内的笔记，目录沿用删除前的归属 */
func (r *NoteRepository) RestoreByBatch(ctx context.Context, userID, batchID string, now time.Time) (int, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		UPDATE notes SET state = $3, deleted_at = NULL, purge_at = NULL,
			delete_batch_id = '', updated_at = $4
		WHERE user_id = $1 AND delete_batch_id = $2 AND deleted_at IS NOT NULL
		RETURNING `+noteColumns,
		userID, batchID, string(entity.NoteStateNormal), now)
	if err != nil {
		return 0, fmt.Errorf("批量恢复笔记: %w", err)
	}
	defer rows.Close()

	affected := 0
	pending := make([]*entity.Note, 0)
	for rows.Next() {
		note, err := scanNote(rows)
		if err != nil {
			return 0, fmt.Errorf("扫描已恢复笔记: %w", err)
		}
		pending = append(pending, note)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历已恢复笔记: %w", err)
	}
	/* 关闭游标后再写同步事件，避免同一连接读写冲突 */
	rows.Close()
	for _, note := range pending {
		affected++
		if err := r.appendEvent(ctx, noteChangeEvent(note, entity.ChangeOpUpdate, now)); err != nil {
			return 0, err
		}
	}
	return affected, nil
}

/* appendEvent 在写事务内追加同步事件 */
func (r *NoteRepository) appendEvent(ctx context.Context, event *entity.ChangeEvent) error {
	if r.changes == nil {
		return nil
	}
	_, err := r.changes.appendEvent(ctx, event)
	return err
}

/* queryNotes 执行查询并映射为笔记实体 */
func (r *NoteRepository) queryNotes(ctx context.Context, query string, args ...any) ([]*entity.Note, error) {
	rows, err := r.db.q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询笔记: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.Note, 0)
	for rows.Next() {
		note, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描笔记: %w", err)
		}
		items = append(items, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历笔记: %w", err)
	}
	return items, nil
}

/* scanNote 把一行映射为笔记实体 */
func scanNote(row rowScanner) (*entity.Note, error) {
	note := &entity.Note{}
	var state string
	err := row.Scan(&note.ID, &note.UserID, &note.FolderID, &note.Title, &note.Content, &state,
		&note.Revision, &note.CharCount, &note.ConflictCopy, &note.DeletedAt,
		&note.DeleteBatchID, &note.PurgeAt, &note.CreatedAt, &note.UpdatedAt)
	if err != nil {
		return nil, err
	}
	note.State = entity.NoteState(state)
	return note, nil
}

/* cursorTimeFromKey 把「最大值减纳秒」形式的排序键还原为时间 */
func cursorTimeFromKey(key string) time.Time {
	value, err := strconv.ParseInt(strings.TrimSpace(key), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, math.MaxInt64-value).UTC()
}

/* parseSearchKey 拆分「命中层级:排序键」，返回层级、时间与是否解析成功 */
func parseSearchKey(key string) (string, time.Time, bool) {
	parts := strings.SplitN(strings.TrimSpace(key), ":", 2)
	if len(parts) != 2 {
		return "", time.Time{}, false
	}
	return parts[0], cursorTimeFromKey(parts[1]), true
}

/* 确保实现端口契约 */
var _ port.NoteRepository = (*NoteRepository)(nil)
