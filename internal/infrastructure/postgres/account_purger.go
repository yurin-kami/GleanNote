/* 账号注销的级联清理：在同一事务内按 user_id 删除全部业务数据 */
package postgres

import (
	"context"
	"fmt"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* AccountPurger 是 port.AccountDataPurger 的 PostgreSQL 实现 */
type AccountPurger struct {
	db *DB
}

/* NewAccountPurger 绑定连接池；事务上下文由调用方提供 */
func NewAccountPurger(db *DB) *AccountPurger { return &AccountPurger{db: db} }

/*
 * purgeStatement 描述一条按账号清理的 SQL。
 * import_items 没有 user_id 列，只能借助所属导入任务定位，因此单列一条子查询。
 */
type purgeStatement struct {
	table string
	sql   string
}

/* purgeStatements 按依赖顺序排列：先删派生数据，再删笔记与目录，最后按邮箱清理验证码 */
var purgeStatements = []purgeStatement{
	{"note_versions", `DELETE FROM note_versions WHERE user_id = $1`},
	{"share_links", `DELETE FROM share_links WHERE user_id = $1`},
	{"note_tags", `DELETE FROM note_tags WHERE user_id = $1`},
	{"conversion_jobs", `DELETE FROM conversion_jobs WHERE user_id = $1`},
	{"note_pages", `DELETE FROM note_pages WHERE user_id = $1`},
	{"quota_charges", `DELETE FROM quota_charges WHERE user_id = $1`},
	{"quota_usage", `DELETE FROM quota_usage WHERE user_id = $1`},
	{"storage_usage", `DELETE FROM storage_usage WHERE user_id = $1`},
	{"uploads", `DELETE FROM uploads WHERE user_id = $1`},
	{"export_jobs", `DELETE FROM export_jobs WHERE user_id = $1`},
	{"import_items", `DELETE FROM import_items WHERE job_id IN
		(SELECT id FROM import_jobs WHERE user_id = $1)`},
	{"import_jobs", `DELETE FROM import_jobs WHERE user_id = $1`},
	{"change_events", `DELETE FROM change_events WHERE user_id = $1`},
	{"change_seq", `DELETE FROM change_seq WHERE user_id = $1`},
	{"change_watermarks", `DELETE FROM change_watermarks WHERE user_id = $1`},
	{"refresh_tokens", `DELETE FROM refresh_tokens WHERE user_id = $1`},
	{"notes", `DELETE FROM notes WHERE user_id = $1`},
	{"folders", `DELETE FROM folders WHERE user_id = $1`},
	{"tags", `DELETE FROM tags WHERE user_id = $1`},
	{"email_codes", `DELETE FROM email_codes WHERE lower(email) =
		(SELECT lower(email) FROM users WHERE id = $1)`},
}

/*
 * PurgeForUser 逐表删除账号数据。
 * 每条语句都带 user_id 条件，借助既有索引批量删除；任一失败即中止整个事务。
 */
func (p *AccountPurger) PurgeForUser(ctx context.Context, userID string) error {
	for _, statement := range purgeStatements {
		if _, err := p.db.q(ctx).Exec(ctx, statement.sql, userID); err != nil {
			return fmt.Errorf("清理账号数据 %s: %w", statement.table, err)
		}
	}
	return nil
}

/* 确保实现端口契约 */
var _ port.AccountDataPurger = (*AccountPurger)(nil)
