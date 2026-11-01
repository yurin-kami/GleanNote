/* 目录树仓储的 PostgreSQL 实现：物化路径 + 键集分页 + 子树批处理 */
package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* FolderRepository 是 port.FolderRepository 的 PostgreSQL 实现 */
type FolderRepository struct {
	db      *DB
	changes *ChangeLogRepository
}

/* NewFolderRepository 绑定连接池与变更日志，写事务内同步追加同步事件 */
func NewFolderRepository(db *DB, changes *ChangeLogRepository) *FolderRepository {
	return &FolderRepository{db: db, changes: changes}
}

/* folderColumns 是读取目录的列清单，顺序与 scanFolder 保持一致 */
const folderColumns = `id, user_id, parent_id, name, depth, path, revision, sort_order,
	deleted_at, delete_batch_id, purge_at, created_at, updated_at`

/* Create 插入目录节点；同级重名映射为 NAME_DUPLICATED */
func (r *FolderRepository) Create(ctx context.Context, folder *entity.Folder) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO folders (`+folderColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		folder.ID, folder.UserID, folder.ParentID, folder.Name, folder.Depth, folder.Path,
		folder.Revision, folder.SortOrder, folder.DeletedAt, folder.DeleteBatchID,
		folder.PurgeAt, folder.CreatedAt, folder.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeNameDuplicated, "同级目录已存在同名目录")
		}
		return fmt.Errorf("创建目录: %w", err)
	}
	return r.appendEvent(ctx, folderChangeEvent(folder, entity.ChangeOpCreate, folder.UpdatedAt))
}

/* GetByID 返回目录原始行（含已删除节点） */
func (r *FolderRepository) GetByID(ctx context.Context, userID, folderID string) (*entity.Folder, error) {
	folder, err := scanFolder(r.db.q(ctx).QueryRow(ctx,
		`SELECT `+folderColumns+` FROM folders WHERE user_id = $1 AND id = $2`, userID, folderID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "目录不存在")
	}
	return folder, nil
}

/*
 * ListChildren 返回未删除的直接子目录。
 * 排序口径与 port.FolderSortKey 一致：创建时间升序或小写名称升序，同键按 ID 升序。
 */
func (r *FolderRepository) ListChildren(ctx context.Context, query port.FolderListQuery) ([]*entity.Folder, int, error) {
	base := `SELECT ` + folderColumns + ` FROM folders
		WHERE user_id = $1 AND deleted_at IS NULL
		AND ($2::text IS NULL AND parent_id IS NULL OR parent_id = $2::text)`

	var total int
	if err := r.db.q(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM (`+base+`) AS filtered`, query.UserID, query.ParentID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计子目录: %w", err)
	}

	nameOrder := query.Order == port.FolderOrderNameAsc
	statement := base
	args := []any{query.UserID, query.ParentID}
	cursor := query.After
	switch {
	case cursor == nil:
	case nameOrder:
		statement += ` AND (lower(name) > $3 OR (lower(name) = $3 AND id > $4))`
		args = append(args, cursor.SortKey, cursor.ID)
	default:
		statement += ` AND (created_at > $3::timestamptz OR (created_at = $3::timestamptz AND id > $4))`
		args = append(args, cursorTime(cursor.SortKey), cursor.ID)
	}
	if nameOrder {
		statement += ` ORDER BY lower(name) ASC, id ASC`
	} else {
		statement += ` ORDER BY created_at ASC, id ASC`
	}
	if query.Limit > 0 {
		statement += fmt.Sprintf(` LIMIT %d`, query.Limit)
	}

	folders, err := r.queryFolders(ctx, statement, args...)
	if err != nil {
		return nil, 0, err
	}
	return folders, total, nil
}

/* CountChildren 统计给定目录的直接子目录数量（仅未删除节点） */
func (r *FolderRepository) CountChildren(ctx context.Context, userID string, parentIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(parentIDs))
	if len(parentIDs) == 0 {
		return counts, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT parent_id, COUNT(*) FROM folders
		WHERE user_id = $1 AND deleted_at IS NULL AND parent_id = ANY($2)
		GROUP BY parent_id`, userID, parentIDs)
	if err != nil {
		return nil, fmt.Errorf("统计直接子目录: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var parentID string
		var count int
		if err := rows.Scan(&parentID, &count); err != nil {
			return nil, fmt.Errorf("扫描子目录计数: %w", err)
		}
		counts[parentID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历子目录计数: %w", err)
	}
	return counts, nil
}

/* NameExists 判断同级是否存在同名未删除目录，excludeID 用于改名时排除自身 */
func (r *FolderRepository) NameExists(ctx context.Context, userID string, parentID *string, name, excludeID string) (bool, error) {
	var exists bool
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM folders
			WHERE user_id = $1 AND deleted_at IS NULL AND id <> $3
			AND lower(name) = lower($4)
			AND ($2::text IS NULL AND parent_id IS NULL OR parent_id = $2::text)
		)`, userID, parentID, excludeID, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查目录重名: %w", err)
	}
	return exists, nil
}

/*
 * LockTree 在事务内以 pg_advisory_xact_lock 串行化同一账号的目录树写入。
 * 约束：必须在 WithinTransaction 内调用；事务级咨询锁在提交/回滚时自动释放。
 */
func (r *FolderRepository) LockTree(ctx context.Context, userID string) error {
	if _, ok := txFrom(ctx); !ok {
		return errs.New(errs.CodeInternal, "目录写入必须在事务内执行")
	}
	if _, err := r.db.q(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "folders:"+userID); err != nil {
		return fmt.Errorf("锁定目录树: %w", err)
	}
	return nil
}

/* Update 覆盖目录行；写入变更事件时依据删除状态判定操作类型 */
func (r *FolderRepository) Update(ctx context.Context, folder *entity.Folder) error {
	_, err := r.write(ctx, folder, -1)
	return err
}

/* UpdateWithRevision 以 revision 条件更新目录行；expected < 0 表示不做乐观锁校验 */
func (r *FolderRepository) UpdateWithRevision(ctx context.Context, folder *entity.Folder, expected int) (bool, error) {
	return r.write(ctx, folder, expected)
}

/*
 * write 是 Update/UpdateWithRevision 的共用实现。
 * expected >= 0 时追加 `AND revision = $expected`，0 行命中即返回 false 且不写同步事件。
 */
func (r *FolderRepository) write(ctx context.Context, folder *entity.Folder, expected int) (bool, error) {
	var previousDeleted *time.Time
	err := r.db.q(ctx).QueryRow(ctx,
		`SELECT deleted_at FROM folders WHERE user_id = $1 AND id = $2`,
		folder.UserID, folder.ID).Scan(&previousDeleted)
	if err != nil {
		return false, mapNotFound(err, errs.CodeResourceNotFound, "目录不存在")
	}
	statement := `UPDATE folders SET parent_id = $3, name = $4, depth = $5, path = $6, revision = $7,
		sort_order = $8, deleted_at = $9, delete_batch_id = $10, purge_at = $11, updated_at = $12
		WHERE user_id = $1 AND id = $2`
	args := []any{folder.UserID, folder.ID, folder.ParentID, folder.Name, folder.Depth, folder.Path,
		folder.Revision, folder.SortOrder, folder.DeletedAt, folder.DeleteBatchID,
		folder.PurgeAt, folder.UpdatedAt}
	if expected >= 0 {
		statement += ` AND revision = $13`
		args = append(args, expected)
	}
	tag, err := r.db.q(ctx).Exec(ctx, statement, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return false, errs.New(errs.CodeNameDuplicated, "同级目录已存在同名目录")
		}
		return false, fmt.Errorf("更新目录: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	op := entity.ChangeOpUpdate
	if previousDeleted == nil && folder.IsDeleted() {
		op = entity.ChangeOpDelete
	}
	if err := r.appendEvent(ctx, folderChangeEvent(folder, op, folder.UpdatedAt)); err != nil {
		return false, err
	}
	return true, nil
}

/* ListExpired 返回 purge_at 已到期的回收站目录，供定时清理任务消费 */
func (r *FolderRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Folder, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.queryFolders(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE deleted_at IS NOT NULL AND purge_at IS NOT NULL AND purge_at <= $1
		ORDER BY purge_at ASC, id ASC LIMIT $2`, now, limit)
}

/* ListSubtree 返回含根节点在内的整棵子树（含回收站节点），按深度与创建时间排序 */
func (r *FolderRepository) ListSubtree(ctx context.Context, userID, folderID string) ([]*entity.Folder, error) {
	root, err := r.GetByID(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}
	return r.queryFolders(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE user_id = $1 AND (path = $2 OR path LIKE $2 || '/%')
		ORDER BY depth ASC, created_at ASC, id ASC`, userID, root.Path)
}

/* ListDeleted 返回用户全部回收站目录，供回收站聚合按批次分组 */
func (r *FolderRepository) ListDeleted(ctx context.Context, userID string) ([]*entity.Folder, error) {
	return r.queryFolders(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE user_id = $1 AND deleted_at IS NOT NULL
		ORDER BY deleted_at ASC, id ASC`, userID)
}

/* Root 返回账号根目录（parent 为空且未删除） */
func (r *FolderRepository) Root(ctx context.Context, userID string) (*entity.Folder, error) {
	folder, err := scanFolder(r.db.q(ctx).QueryRow(ctx, `SELECT `+folderColumns+` FROM folders
		WHERE user_id = $1 AND parent_id IS NULL AND deleted_at IS NULL
		ORDER BY created_at ASC LIMIT 1`, userID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "账号根目录不存在")
	}
	return folder, nil
}

/* SaveBatch 批量覆盖目录行；任一节点缺失即整体失败（调用方处于事务内） */
func (r *FolderRepository) SaveBatch(ctx context.Context, folders []*entity.Folder) error {
	for _, folder := range folders {
		if err := r.Update(ctx, folder); err != nil {
			return err
		}
	}
	return nil
}

/*
 * DeleteBatch 物理删除子树上与根节点同一删除批次的行，返回删除数量。
 * 删除前先取回被删行，以便逐条写入墓碑事件。
 */
func (r *FolderRepository) DeleteBatch(ctx context.Context, userID, folderID string) (int, error) {
	root, err := r.GetByID(ctx, userID, folderID)
	if err != nil {
		return 0, err
	}
	if root.DeleteBatchID == "" {
		return 0, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		DELETE FROM folders
		WHERE user_id = $1 AND delete_batch_id = $2
		AND (path = $3 OR path LIKE $3 || '/%')
		RETURNING `+folderColumns, userID, root.DeleteBatchID, root.Path)
	if err != nil {
		return 0, fmt.Errorf("删除目录批次: %w", err)
	}
	defer rows.Close()

	now := nowFunc()
	removed := 0
	pending := make([]*entity.Folder, 0)
	for rows.Next() {
		folder, err := scanFolder(rows)
		if err != nil {
			return 0, fmt.Errorf("扫描已删除目录: %w", err)
		}
		pending = append(pending, folder)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历已删除目录: %w", err)
	}
	/* 关闭游标后再逐条写墓碑事件：pgx 同一连接不允许边读行边执行新语句 */
	rows.Close()
	for _, folder := range pending {
		removed++
		if err := r.appendEvent(ctx, folderChangeEvent(folder, entity.ChangeOpDelete, now)); err != nil {
			return 0, err
		}
	}
	return removed, nil
}

/* DeleteAllByUser 清除账号全部目录；注销用例在一个事务内调用 */
func (r *FolderRepository) DeleteAllByUser(ctx context.Context, userID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM folders WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("删除账号目录: %w", err)
	}
	return nil
}

/* appendEvent 在写事务内追加同步事件；未绑定日志时静默跳过 */
func (r *FolderRepository) appendEvent(ctx context.Context, event *entity.ChangeEvent) error {
	if r.changes == nil {
		return nil
	}
	_, err := r.changes.appendEvent(ctx, event)
	return err
}

/* queryFolders 执行查询并映射为目录实体 */
func (r *FolderRepository) queryFolders(ctx context.Context, query string, args ...any) ([]*entity.Folder, error) {
	rows, err := r.db.q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询目录: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.Folder, 0)
	for rows.Next() {
		folder, err := scanFolder(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描目录: %w", err)
		}
		items = append(items, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历目录: %w", err)
	}
	return items, nil
}

/* rowScanner 抽象单行与多行扫描 */
type rowScanner interface {
	Scan(dest ...any) error
}

/* scanFolder 把一行映射为目录实体 */
func scanFolder(row rowScanner) (*entity.Folder, error) {
	folder := &entity.Folder{}
	err := row.Scan(&folder.ID, &folder.UserID, &folder.ParentID, &folder.Name, &folder.Depth,
		&folder.Path, &folder.Revision, &folder.SortOrder, &folder.DeletedAt,
		&folder.DeleteBatchID, &folder.PurgeAt, &folder.CreatedAt, &folder.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return folder, nil
}

/* cursorTime 把分页令牌的排序键解析为时间；解析失败视为零值，交由查询自然收敛 */
func cursorTime(key string) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(key)); err == nil {
		return parsed
	}
	return time.Time{}
}

/* 确保实现端口契约 */
var _ port.FolderRepository = (*FolderRepository)(nil)
