/* 标签本体与笔记标签关联的 PostgreSQL 实现 */
package postgres

import (
	"context"
	"fmt"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TagRepository 是 port.TagRepository 的 PostgreSQL 实现 */
type TagRepository struct {
	db      *DB
	changes *ChangeLogRepository
}

/* NewTagRepository 绑定连接池与变更日志 */
func NewTagRepository(db *DB, changes *ChangeLogRepository) *TagRepository {
	return &TagRepository{db: db, changes: changes}
}

/* Create 插入标签；账号内重名映射为 NAME_DUPLICATED */
func (r *TagRepository) Create(ctx context.Context, tag *entity.Tag) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO tags (id, user_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)`,
		tag.ID, tag.UserID, tag.Name, tag.CreatedAt, tag.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeNameDuplicated, "标签已存在")
		}
		return fmt.Errorf("创建标签: %w", err)
	}
	return r.appendEvent(ctx, tagChangeEvent(tag, entity.ChangeOpCreate, tag.UpdatedAt))
}

/* GetByID 按属主查询标签 */
func (r *TagRepository) GetByID(ctx context.Context, userID, tagID string) (*entity.Tag, error) {
	return scanTag(r.db.q(ctx).QueryRow(ctx, `
		SELECT id, user_id, name, created_at, updated_at FROM tags
		WHERE user_id = $1 AND id = $2`, userID, tagID))
}

/* List 返回账号全部标签，按创建时间升序 */
func (r *TagRepository) List(ctx context.Context, userID string) ([]*entity.Tag, error) {
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT id, user_id, name, created_at, updated_at FROM tags
		WHERE user_id = $1 ORDER BY created_at ASC, id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询标签: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.Tag, 0)
	for rows.Next() {
		tag, err := scanTag(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描标签: %w", err)
		}
		items = append(items, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历标签: %w", err)
	}
	return items, nil
}

/* Update 覆盖标签名称并追加同步事件 */
func (r *TagRepository) Update(ctx context.Context, tag *entity.Tag) error {
	tagRow, err := r.db.q(ctx).Exec(ctx, `
		UPDATE tags SET name = $3, updated_at = $4 WHERE user_id = $1 AND id = $2`,
		tag.UserID, tag.ID, tag.Name, tag.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeNameDuplicated, "标签已存在")
		}
		return fmt.Errorf("更新标签: %w", err)
	}
	if tagRow.RowsAffected() == 0 {
		return errs.New(errs.CodeResourceNotFound, "标签不存在")
	}
	return r.appendEvent(ctx, tagChangeEvent(tag, entity.ChangeOpUpdate, tag.UpdatedAt))
}

/* Delete 删除标签并追加墓碑事件 */
func (r *TagRepository) Delete(ctx context.Context, userID, tagID string) error {
	tag, err := r.GetByID(ctx, userID, tagID)
	if err != nil {
		return err
	}
	if _, err := r.db.q(ctx).Exec(ctx,
		`DELETE FROM tags WHERE user_id = $1 AND id = $2`, userID, tagID); err != nil {
		return fmt.Errorf("删除标签: %w", err)
	}
	return r.appendEvent(ctx, tagChangeEvent(tag, entity.ChangeOpDelete, nowFunc()))
}

/* DeleteAllByUser 清除账号全部标签；注销用例在一个事务内调用 */
func (r *TagRepository) DeleteAllByUser(ctx context.Context, userID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM tags WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("删除账号标签: %w", err)
	}
	return nil
}

/* appendEvent 在写事务内追加同步事件 */
func (r *TagRepository) appendEvent(ctx context.Context, event *entity.ChangeEvent) error {
	if r.changes == nil {
		return nil
	}
	_, err := r.changes.appendEvent(ctx, event)
	return err
}

/* scanTag 把一行映射为标签实体 */
func scanTag(row rowScanner) (*entity.Tag, error) {
	tag := &entity.Tag{}
	err := row.Scan(&tag.ID, &tag.UserID, &tag.Name, &tag.CreatedAt, &tag.UpdatedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "标签不存在")
	}
	return tag, nil
}

/* NoteTagRepository 是 port.NoteTagRepository 的 PostgreSQL 实现 */
type NoteTagRepository struct {
	db      *DB
	changes *ChangeLogRepository
}

/* NewNoteTagRepository 绑定连接池与变更日志 */
func NewNoteTagRepository(db *DB, changes *ChangeLogRepository) *NoteTagRepository {
	return &NoteTagRepository{db: db, changes: changes}
}

/* ReplaceForNote 整体覆盖一篇笔记的标签并追加关联变更事件 */
func (r *NoteTagRepository) ReplaceForNote(ctx context.Context, userID, noteID string, tagIDs []string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `
		DELETE FROM note_tags WHERE user_id = $1 AND note_id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("清除笔记标签: %w", err)
	}
	if len(tagIDs) > 0 {
		if _, err := r.db.q(ctx).Exec(ctx, `
			INSERT INTO note_tags (user_id, note_id, tag_id)
			SELECT $1, $2, unnest($3::text[])
			ON CONFLICT (note_id, tag_id) DO NOTHING`, userID, noteID, tagIDs); err != nil {
			return fmt.Errorf("写入笔记标签: %w", err)
		}
	}
	if r.changes == nil {
		return nil
	}
	_, err := r.changes.appendEvent(ctx, noteTagsChangeEvent(userID, noteID, tagIDs, nowFunc()))
	return err
}

/* TagsByNotes 批量返回多篇笔记的标签集合 */
func (r *NoteTagRepository) TagsByNotes(ctx context.Context, userID string, noteIDs []string) (map[string][]*entity.Tag, error) {
	result := make(map[string][]*entity.Tag, len(noteIDs))
	if len(noteIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT nt.note_id, t.id, t.user_id, t.name, t.created_at, t.updated_at
		FROM note_tags nt JOIN tags t ON t.id = nt.tag_id AND t.user_id = nt.user_id
		WHERE nt.user_id = $1 AND nt.note_id = ANY($2)
		ORDER BY nt.note_id ASC, t.created_at ASC, t.id ASC`, userID, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("查询笔记标签: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var noteID string
		tag := &entity.Tag{}
		if err := rows.Scan(&noteID, &tag.ID, &tag.UserID, &tag.Name, &tag.CreatedAt, &tag.UpdatedAt); err != nil {
			return nil, fmt.Errorf("扫描笔记标签: %w", err)
		}
		result[noteID] = append(result[noteID], tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历笔记标签: %w", err)
	}
	return result, nil
}

/* NoteIDsByTags 返回同时包含全部给定标签的笔记 ID */
func (r *NoteTagRepository) NoteIDsByTags(ctx context.Context, userID string, tagIDs []string) ([]string, error) {
	if len(tagIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT note_id FROM note_tags
		WHERE user_id = $1 AND tag_id = ANY($2)
		GROUP BY note_id HAVING COUNT(DISTINCT tag_id) = $3
		ORDER BY note_id ASC`, userID, tagIDs, len(tagIDs))
	if err != nil {
		return nil, fmt.Errorf("查询标签笔记: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var noteID string
		if err := rows.Scan(&noteID); err != nil {
			return nil, fmt.Errorf("扫描标签笔记: %w", err)
		}
		ids = append(ids, noteID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历标签笔记: %w", err)
	}
	return ids, nil
}

/* CountsByTags 统计未删除笔记的标签命中数；tagIDs 为空表示统计全部标签 */
func (r *NoteTagRepository) CountsByTags(ctx context.Context, userID string, tagIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(tagIDs))
	statement := `SELECT nt.tag_id, COUNT(DISTINCT n.id)
		FROM note_tags nt JOIN notes n ON n.id = nt.note_id AND n.user_id = nt.user_id
		WHERE nt.user_id = $1 AND n.deleted_at IS NULL`
	args := []any{userID}
	if tagIDs != nil {
		statement += ` AND nt.tag_id = ANY($2)`
		args = append(args, tagIDs)
	}
	statement += ` GROUP BY nt.tag_id`

	rows, err := r.db.q(ctx).Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("统计标签笔记数: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tagID string
		var count int
		if err := rows.Scan(&tagID, &count); err != nil {
			return nil, fmt.Errorf("扫描标签计数: %w", err)
		}
		counts[tagID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历标签计数: %w", err)
	}
	return counts, nil
}

/* DeleteByTag 删除标签的全部关联 */
func (r *NoteTagRepository) DeleteByTag(ctx context.Context, userID, tagID string) error {
	if _, err := r.db.q(ctx).Exec(ctx,
		`DELETE FROM note_tags WHERE user_id = $1 AND tag_id = $2`, userID, tagID); err != nil {
		return fmt.Errorf("删除标签关联: %w", err)
	}
	return nil
}

/* DeleteByNote 删除笔记的全部标签关联 */
func (r *NoteTagRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	if _, err := r.db.q(ctx).Exec(ctx,
		`DELETE FROM note_tags WHERE user_id = $1 AND note_id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("删除笔记标签关联: %w", err)
	}
	return nil
}

/* DeleteAllByUser 清除账号全部标签关联；注销用例在一个事务内调用 */
func (r *NoteTagRepository) DeleteAllByUser(ctx context.Context, userID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM note_tags WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("删除账号标签关联: %w", err)
	}
	return nil
}

/* 确保实现端口契约 */
var (
	_ port.TagRepository     = (*TagRepository)(nil)
	_ port.NoteTagRepository = (*NoteTagRepository)(nil)
)
