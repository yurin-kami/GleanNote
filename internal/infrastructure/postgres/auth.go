/* 账号、验证码与刷新令牌仓储的 PostgreSQL 实现 */
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* UserRepository 是 port.UserRepository 的 PostgreSQL 实现 */
type UserRepository struct {
	db *DB
}

/* NewUserRepository 绑定连接池 */
func NewUserRepository(db *DB) *UserRepository { return &UserRepository{db: db} }

/* Create 插入账号；邮箱唯一冲突映射为 AUTH_EMAIL_ALREADY_EXISTS */
func (r *UserRepository) Create(ctx context.Context, user *entity.User) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO users (id, email, password_hash, nickname, status, email_verified_at,
			created_at, updated_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		user.ID, user.Email, user.PasswordHash, user.Nickname, user.Status,
		user.EmailVerifiedAt, user.CreatedAt, user.UpdatedAt, user.DeletedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeAuthEmailAlreadyExists, "该邮箱已注册")
		}
		return fmt.Errorf("创建用户: %w", err)
	}
	return nil
}

/* GetByEmail 按大小写不敏感邮箱查询未删除账号 */
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*entity.User, error) {
	return r.scanUser(ctx, `SELECT id, email, password_hash, nickname, status, email_verified_at,
		created_at, updated_at, deleted_at FROM users WHERE lower(email) = lower($1) AND deleted_at IS NULL`, email)
}

/* GetByID 按主键查询未删除账号 */
func (r *UserRepository) GetByID(ctx context.Context, userID string) (*entity.User, error) {
	return r.scanUser(ctx, `SELECT id, email, password_hash, nickname, status, email_verified_at,
		created_at, updated_at, deleted_at FROM users WHERE id = $1 AND deleted_at IS NULL`, userID)
}

/* Update 覆盖账号资料 */
func (r *UserRepository) Update(ctx context.Context, user *entity.User) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE users SET email = $2, password_hash = $3, nickname = $4, status = $5,
			email_verified_at = $6, updated_at = $7, deleted_at = $8
		WHERE id = $1`,
		user.ID, user.Email, user.PasswordHash, user.Nickname, user.Status,
		user.EmailVerifiedAt, user.UpdatedAt, user.DeletedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeAuthEmailAlreadyExists, "该邮箱已注册")
		}
		return fmt.Errorf("更新用户: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	return nil
}

/* Delete 物理删除账号；注销流程在一个事务内清理全部归属数据 */
func (r *UserRepository) Delete(ctx context.Context, userID string) error {
	tag, err := r.db.q(ctx).Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("删除用户: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	return nil
}

/* scanUser 执行单行查询并映射账号实体 */
func (r *UserRepository) scanUser(ctx context.Context, query string, args ...any) (*entity.User, error) {
	user := &entity.User{}
	err := r.db.q(ctx).QueryRow(ctx, query, args...).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Nickname, &user.Status,
		&user.EmailVerifiedAt, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "用户不存在")
	}
	return user, nil
}

/* EmailCodeRepository 是 port.EmailCodeRepository 的 PostgreSQL 实现 */
type EmailCodeRepository struct {
	db *DB
}

/* NewEmailCodeRepository 绑定连接池 */
func NewEmailCodeRepository(db *DB) *EmailCodeRepository { return &EmailCodeRepository{db: db} }

/* Create 插入一次验证码发送记录 */
func (r *EmailCodeRepository) Create(ctx context.Context, code *entity.EmailCode) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO email_codes (id, email, purpose, code_hash, expires_at, attempts, consumed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		code.ID, code.Email, code.Purpose, code.CodeHash, code.ExpiresAt,
		code.Attempts, code.ConsumedAt, code.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建验证码: %w", err)
	}
	return nil
}

/* LatestActive 返回同邮箱同用途最近一条未消费记录 */
func (r *EmailCodeRepository) LatestActive(ctx context.Context, email, purpose string) (*entity.EmailCode, error) {
	code := &entity.EmailCode{}
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT id, email, purpose, code_hash, expires_at, attempts, consumed_at, created_at
		FROM email_codes
		WHERE lower(email) = lower($1) AND purpose = $2 AND consumed_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, email, purpose).Scan(
		&code.ID, &code.Email, &code.Purpose, &code.CodeHash, &code.ExpiresAt,
		&code.Attempts, &code.ConsumedAt, &code.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "验证码不存在")
	}
	return code, nil
}

/* Update 覆盖错误次数与消费时间 */
func (r *EmailCodeRepository) Update(ctx context.Context, code *entity.EmailCode) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE email_codes SET attempts = $2, consumed_at = $3 WHERE id = $1`,
		code.ID, code.Attempts, code.ConsumedAt)
	if err != nil {
		return fmt.Errorf("更新验证码: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CodeResourceNotFound, "验证码不存在")
	}
	return nil
}

/* InvalidateActive 把同邮箱同用途的未消费验证码置为已消费 */
func (r *EmailCodeRepository) InvalidateActive(ctx context.Context, email, purpose string, now time.Time) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		UPDATE email_codes SET consumed_at = $3
		WHERE lower(email) = lower($1) AND purpose = $2 AND consumed_at IS NULL`, email, purpose, now)
	if err != nil {
		return fmt.Errorf("失效验证码: %w", err)
	}
	return nil
}

/* RefreshTokenRepository 是 port.RefreshTokenRepository 的 PostgreSQL 实现 */
type RefreshTokenRepository struct {
	db *DB
}

/* NewRefreshTokenRepository 绑定连接池 */
func NewRefreshTokenRepository(db *DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

/* Create 插入刷新令牌哈希 */
func (r *RefreshTokenRepository) Create(ctx context.Context, token *entity.RefreshToken) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, token_hash, device_id, expires_at, revoked_at, replaced_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		token.ID, token.UserID, token.TokenHash, token.DeviceID, token.ExpiresAt,
		token.RevokedAt, token.ReplacedBy, token.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建刷新令牌: %w", err)
	}
	return nil
}

/* GetByHash 按哈希读取令牌，供退出与重放探测使用 */
func (r *RefreshTokenRepository) GetByHash(ctx context.Context, tokenHash string) (*entity.RefreshToken, error) {
	return r.read(ctx, tokenHash, "")
}

/*
 * GetForUpdate 在事务内按哈希锁定令牌行，保证并发刷新只有一个能完成轮换。
 * 未处于事务上下文时返回错误，避免调用方误以为已加锁。
 */
func (r *RefreshTokenRepository) GetForUpdate(ctx context.Context, tokenHash string) (*entity.RefreshToken, error) {
	if _, ok := txFrom(ctx); !ok {
		return nil, errs.New(errs.CodeInternal, "刷新令牌轮换必须在事务内执行")
	}
	return r.read(ctx, tokenHash, " FOR UPDATE")
}

/* read 按哈希读取令牌；suffix 控制是否追加行锁 */
func (r *RefreshTokenRepository) read(ctx context.Context, tokenHash, suffix string) (*entity.RefreshToken, error) {
	token := &entity.RefreshToken{}
	err := r.db.q(ctx).QueryRow(ctx, `
		SELECT id, user_id, token_hash, device_id, expires_at, revoked_at, replaced_by, created_at
		FROM refresh_tokens WHERE token_hash = $1`+suffix, tokenHash).Scan(
		&token.ID, &token.UserID, &token.TokenHash, &token.DeviceID, &token.ExpiresAt,
		&token.RevokedAt, &token.ReplacedBy, &token.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "刷新令牌不存在")
	}
	return token, nil
}

/*
 * Update 仅在令牌未被吊销时写入吊销状态与替换指向。
 * 返回 false 表示该令牌已被并发请求轮换或吊销，调用方应按重放处理。
 */
func (r *RefreshTokenRepository) Update(ctx context.Context, token *entity.RefreshToken) (bool, error) {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $2, replaced_by = $3
		WHERE id = $1 AND revoked_at IS NULL`,
		token.ID, token.RevokedAt, token.ReplacedBy)
	if err != nil {
		return false, fmt.Errorf("更新刷新令牌: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

/* RevokeAll 吊销账号下全部未吊销令牌 */
func (r *RefreshTokenRepository) RevokeAll(ctx context.Context, userID string, now time.Time) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now)
	if err != nil {
		return fmt.Errorf("吊销账号令牌: %w", err)
	}
	return nil
}

/* RevokeDevice 吊销指定设备的全部未吊销令牌 */
func (r *RefreshTokenRepository) RevokeDevice(ctx context.Context, userID, deviceID string, now time.Time) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $3
		WHERE user_id = $1 AND device_id = $2 AND revoked_at IS NULL`, userID, deviceID, now)
	if err != nil {
		return fmt.Errorf("吊销设备令牌: %w", err)
	}
	return nil
}

/* 确保实现端口契约 */
var (
	_ port.UserRepository         = (*UserRepository)(nil)
	_ port.EmailCodeRepository    = (*EmailCodeRepository)(nil)
	_ port.RefreshTokenRepository = (*RefreshTokenRepository)(nil)
)
