/*
 * 认证模块的能力端口：密码、验证码摘要、访问令牌、限流、失败守卫与幂等。
 *
 * 职责：把安全相关的实现细节（Argon2id、HMAC、JWT、Redis）隔离在基础设施层。
 * 不负责：业务流程编排；端口不感知用户、额度与订单等业务状态。
 * 依赖：domain/errs、标准库；实现位于 infrastructure。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权、docs/ARCHITECTURE.md#87-限流与防滥用
 */

package port

import (
	"context"
	"errors"
	"time"
)

/* PasswordHasher 负责口令哈希与校验；实现必须使用 Argon2id 等内存困难算法 */
type PasswordHasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, encoded, plain string) (bool, error)
}

/*
 * CodeHasher 生成带服务端密钥的摘要。
 * 用途：验证码摘要（绑定邮箱、用途与验证码），以及限流键的邮箱脱敏。
 * 约束：调用方必须传入已归一化的值；摘要不可逆且对同一输入确定。
 */
type CodeHasher interface {
	Digest(parts ...string) string
}

/* AccessClaims 是访问令牌校验通过后的最小上下文 */
type AccessClaims struct {
	UserID    string
	DeviceID  string
	TokenID   string
	ExpiresAt time.Time
}

/*
 * 访问令牌签名与校验的端口。
 * 签发返回令牌与过期时刻；校验失败必须返回下面的哨兵错误。
 */
type AccessTokenIssuer interface {
	Issue(userID, deviceID, tokenID string, now time.Time) (string, time.Time, error)
}

/* AccessTokenVerifier 只供鉴权中间件使用，避免传输层依赖签发能力 */
type AccessTokenVerifier interface {
	Verify(raw string, now time.Time) (AccessClaims, error)
}

/* 令牌校验的两类归一错误；上层据此区分 401 的 code */
var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

/*
 * AccessTokenDenylist 保存已登出的令牌 ID，TTL 等于令牌剩余有效期。
 * 实现约定：键只存 jti，不存原始令牌。
 */
type AccessTokenDenylist interface {
	Add(ctx context.Context, tokenID string, expiresAt time.Time) error
	Contains(ctx context.Context, tokenID string) (bool, error)
}

/*
 * RateLimiter 是窗口计数端口。
 * 返回 allowed=false 时同时给出建议重试秒数，供 429 响应使用。
 */
type RateLimiter interface {
	Take(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error)
}

/*
 * FailureGuard 记录连续失败并在达到阈值后锁定。
 * Locked 查询当前锁定状态；Fail 记录一次失败并返回是否已触发锁定；Clear 在成功后清零。
 */
type FailureGuard interface {
	Locked(ctx context.Context, key string) (bool, time.Duration, error)
	Fail(ctx context.Context, key string, max int, lockFor time.Duration) (bool, time.Duration, error)
	Clear(ctx context.Context, key string) error
}

/*
 * IdempotencyRecord 是首次请求的响应快照。
 * 重放时按原状态码与响应体返回，保证创建类接口的语义稳定。
 */
type IdempotencyRecord struct {
	RequestHash string
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}

/*
 * IdempotencyStore 以「作用域 + 键」保存响应快照。
 * Get 未命中返回 nil, nil；同键不同请求体由调用方判定为 IDEMPOTENCY_KEY_REUSED。
 * Reserve 以 SETNX 语义写入 Status=0 的占位记录，返回 false 表示同键请求已在处理；
 * Release 只在记录仍是同一请求的占位时删除，避免误删已完成的快照。
 */
type IdempotencyStore interface {
	Get(ctx context.Context, scope, key string) (*IdempotencyRecord, error)
	Put(ctx context.Context, scope, key string, record *IdempotencyRecord, ttl time.Duration) error
	Reserve(ctx context.Context, scope, key string, record *IdempotencyRecord, ttl time.Duration) (bool, error)
	Release(ctx context.Context, scope, key, requestHash string) error
}

/* Pending 判断幂等记录是否为「正在执行」的占位 */
func (r *IdempotencyRecord) Pending() bool { return r != nil && r.Status == 0 }
