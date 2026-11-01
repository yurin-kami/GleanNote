/* 版本快照与分享链接的 PostgreSQL 实现 */
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* NoteVersionRepository 是 port.NoteVersionRepository 的 PostgreSQL 实现 */
type NoteVersionRepository struct {
	db *DB
}

/* NewNoteVersionRepository 绑定连接池 */
func NewNoteVersionRepository(db *DB) *NoteVersionRepository {
	return &NoteVersionRepository{db: db}
}

/* versionColumns 是读取版本的列清单 */
const versionColumns = `id, user_id, note_id, source, content, char_count, created_at, expires_at`

/* Create 插入正文快照 */
func (r *NoteVersionRepository) Create(ctx context.Context, version *entity.NoteVersion) error {
	_, err := r.db.q(ctx).Exec(ctx, `INSERT INTO note_versions (`+versionColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		version.ID, version.UserID, version.NoteID, string(version.Source), version.Content,
		version.CharCount, version.CreatedAt, version.ExpiresAt)
	if err != nil {
		return fmt.Errorf("创建版本快照: %w", err)
	}
	return nil
}

/* GetByID 按属主、笔记与版本 ID 查询 */
func (r *NoteVersionRepository) GetByID(ctx context.Context, userID, noteID, versionID string) (*entity.NoteVersion, error) {
	version, err := scanVersion(r.db.q(ctx).QueryRow(ctx, `SELECT `+versionColumns+` FROM note_versions
		WHERE user_id = $1 AND note_id = $2 AND id = $3`, userID, noteID, versionID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "版本不存在")
	}
	return version, nil
}

/* ListByNote 按生成时间倒序返回版本 */
func (r *NoteVersionRepository) ListByNote(ctx context.Context, userID, noteID string) ([]*entity.NoteVersion, error) {
	rows, err := r.db.q(ctx).Query(ctx, `SELECT `+versionColumns+` FROM note_versions
		WHERE user_id = $1 AND note_id = $2 ORDER BY created_at DESC, id DESC`, userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("查询版本列表: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.NoteVersion, 0)
	for rows.Next() {
		version, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描版本: %w", err)
		}
		items = append(items, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历版本: %w", err)
	}
	return items, nil
}

/* DeleteByNote 删除笔记全部版本 */
func (r *NoteVersionRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM note_versions
		WHERE user_id = $1 AND note_id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("删除笔记版本: %w", err)
	}
	return nil
}

/* Prune 删除超出保留数量与保留期的版本，返回删除条数 */
func (r *NoteVersionRepository) Prune(ctx context.Context, userID, noteID string, keep int, cutoff time.Time) (int, error) {
	if keep < 0 {
		keep = 0
	}
	tag, err := r.db.q(ctx).Exec(ctx, `
		DELETE FROM note_versions
		WHERE user_id = $1 AND note_id = $2
		AND (created_at < $4 OR id NOT IN (
			SELECT id FROM note_versions WHERE user_id = $1 AND note_id = $2
			ORDER BY created_at DESC, id DESC LIMIT $3))`, userID, noteID, keep, cutoff)
	if err != nil {
		return 0, fmt.Errorf("清理版本: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

/* scanVersion 把一行映射为版本实体 */
func scanVersion(row rowScanner) (*entity.NoteVersion, error) {
	version := &entity.NoteVersion{}
	var source string
	err := row.Scan(&version.ID, &version.UserID, &version.NoteID, &source, &version.Content,
		&version.CharCount, &version.CreatedAt, &version.ExpiresAt)
	if err != nil {
		return nil, err
	}
	version.Source = entity.VersionSource(source)
	return version, nil
}

/* ShareLinkRepository 是 port.ShareLinkRepository 的 PostgreSQL 实现 */
type ShareLinkRepository struct {
	db *DB
}

/* NewShareLinkRepository 绑定连接池 */
func NewShareLinkRepository(db *DB) *ShareLinkRepository {
	return &ShareLinkRepository{db: db}
}

/* GetByNote 返回笔记分享状态；从未开启时返回 RESOURCE_NOT_FOUND */
func (r *ShareLinkRepository) GetByNote(ctx context.Context, userID, noteID string) (*entity.ShareLink, error) {
	link, err := scanShareLink(r.db.q(ctx).QueryRow(ctx, `
		SELECT user_id, note_id, token, enabled, created_at, updated_at FROM share_links
		WHERE user_id = $1 AND note_id = $2`, userID, noteID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "分享链接不存在")
	}
	return link, nil
}

/* GetByToken 按令牌查询，供匿名公开读取使用；令牌不存在返回 RESOURCE_NOT_FOUND，
 * 由用例层统一归一为 410 SHARE_LINK_INACTIVE（与内存实现保持一致） */
func (r *ShareLinkRepository) GetByToken(ctx context.Context, token string) (*entity.ShareLink, error) {
	link, err := scanShareLink(r.db.q(ctx).QueryRow(ctx, `
		SELECT user_id, note_id, token, enabled, created_at, updated_at FROM share_links
		WHERE token = $1 AND token <> ''`, token))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "分享链接不存在")
	}
	return link, nil
}

/* Save 以 (user_id, note_id) 为主键进行 upsert */
func (r *ShareLinkRepository) Save(ctx context.Context, link *entity.ShareLink) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO share_links (user_id, note_id, token, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, note_id) DO UPDATE
			SET token = EXCLUDED.token, enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at`,
		link.UserID, link.NoteID, link.Token, link.Enabled, link.CreatedAt, link.UpdatedAt)
	if err != nil {
		return fmt.Errorf("保存分享链接: %w", err)
	}
	return nil
}

/* Delete 删除笔记分享记录 */
func (r *ShareLinkRepository) Delete(ctx context.Context, userID, noteID string) error {
	if _, err := r.db.q(ctx).Exec(ctx,
		`DELETE FROM share_links WHERE user_id = $1 AND note_id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("删除分享链接: %w", err)
	}
	return nil
}

/* DeleteByNote 与 Delete 同义，满足笔记用例清理分享链接的最小接口 */
func (r *ShareLinkRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	return r.Delete(ctx, userID, noteID)
}

/* EnabledByNotes 批量返回笔记的分享开启状态 */
func (r *ShareLinkRepository) EnabledByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(noteIDs))
	if len(noteIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT note_id, enabled FROM share_links
		WHERE user_id = $1 AND note_id = ANY($2)`, userID, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("查询分享状态: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var noteID string
		var enabled bool
		if err := rows.Scan(&noteID, &enabled); err != nil {
			return nil, fmt.Errorf("扫描分享状态: %w", err)
		}
		result[noteID] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历分享状态: %w", err)
	}
	return result, nil
}

/* scanShareLink 把一行映射为分享状态 */
func scanShareLink(row rowScanner) (*entity.ShareLink, error) {
	link := &entity.ShareLink{}
	err := row.Scan(&link.UserID, &link.NoteID, &link.Token, &link.Enabled,
		&link.CreatedAt, &link.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return link, nil
}

/* 确保实现端口契约 */
var (
	_ port.NoteVersionRepository = (*NoteVersionRepository)(nil)
	_ port.ShareLinkRepository   = (*ShareLinkRepository)(nil)
)
