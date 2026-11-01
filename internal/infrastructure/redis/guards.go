/* 限流、失败守卫、幂等与令牌黑名单的 Redis 实现 */
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* RateLimiter 是 port.RateLimiter 的固定窗口实现 */
type RateLimiter struct {
	client *Client
}

/* NewRateLimiter 绑定共享客户端 */
func NewRateLimiter(client *Client) *RateLimiter { return &RateLimiter{client: client} }

/*
 * Take 在窗口内计数。
 * 键按窗口起点分桶，窗口结束即可重试；未超限返回 allowed=true。
 */
func (l *RateLimiter) Take(ctx context.Context, keyName string, limit int, window time.Duration) (bool, time.Duration, error) {
	if limit <= 0 || window <= 0 {
		return true, 0, nil
	}
	now := nowFunc()
	bucket := now.Truncate(window)
	redisKey := key("rl", keyName, strconv.FormatInt(bucket.Unix(), 10))

	pipe := l.client.rdb.TxPipeline()
	incr := pipe.Incr(ctx, redisKey)
	pipe.PExpire(ctx, redisKey, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("限流计数: %w", err)
	}
	if incr.Val() <= int64(limit) {
		return true, 0, nil
	}
	/* 剩余窗口按注入时钟计算，保证测试替身与生产语义一致（G-09） */
	return false, bucket.Add(window).Sub(now), nil
}

/* FailureGuard 是 port.FailureGuard 的 Redis 实现，按对象累计失败并锁定 */
type FailureGuard struct {
	client *Client
}

/* NewFailureGuard 绑定共享客户端 */
func NewFailureGuard(client *Client) *FailureGuard { return &FailureGuard{client: client} }

/* Locked 查询当前锁定状态与剩余时长 */
func (g *FailureGuard) Locked(ctx context.Context, keyName string) (bool, time.Duration, error) {
	redisKey := key("fg", keyName)
	raw, err := g.client.rdb.HGet(ctx, redisKey, "lockUntil").Result()
	if err != nil {
		if err == goredis.Nil {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("查询锁定状态: %w", err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, 0, fmt.Errorf("解析锁定时间: %w", err)
	}
	if remaining := deadline.Sub(nowFunc()); remaining > 0 {
		return true, remaining, nil
	}
	return false, 0, nil
}

/*
 * Fail 记录一次失败；达到阈值即锁定并返回 locked=true。
 * 锁定窗口取 lockFor，计数保留至窗口结束，成功路径由 Clear 清零。
 */
func (g *FailureGuard) Fail(ctx context.Context, keyName string, max int, lockFor time.Duration) (bool, time.Duration, error) {
	redisKey := key("fg", keyName)
	pipe := g.client.rdb.TxPipeline()
	count := pipe.HIncrBy(ctx, redisKey, "count", 1)
	pipe.PExpire(ctx, redisKey, lockFor)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("记录失败次数: %w", err)
	}
	if max > 0 && count.Val() >= int64(max) {
		deadline := nowFunc().Add(lockFor)
		if err := g.client.rdb.HSet(ctx, redisKey, "lockUntil", deadline.Format(time.RFC3339Nano)).Err(); err != nil {
			return false, 0, fmt.Errorf("写入锁定时间: %w", err)
		}
		return true, lockFor, nil
	}
	return false, 0, nil
}

/* Clear 在成功后清零计数与锁定 */
func (g *FailureGuard) Clear(ctx context.Context, keyName string) error {
	if err := g.client.rdb.Del(ctx, key("fg", keyName)).Err(); err != nil {
		return fmt.Errorf("清除失败计数: %w", err)
	}
	return nil
}

/* IdempotencyStore 是 port.IdempotencyStore 的 Redis 实现，以 JSON 保存响应快照 */
type IdempotencyStore struct {
	client *Client
}

/* NewIdempotencyStore 绑定共享客户端 */
func NewIdempotencyStore(client *Client) *IdempotencyStore {
	return &IdempotencyStore{client: client}
}

/* Get 读取响应快照；未命中返回 nil, nil */
func (s *IdempotencyStore) Get(ctx context.Context, scope, keyName string) (*port.IdempotencyRecord, error) {
	raw, err := s.client.rdb.Get(ctx, key("idem", scope, keyName)).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("读取幂等记录: %w", err)
	}
	record := &port.IdempotencyRecord{}
	if err := json.Unmarshal(raw, record); err != nil {
		return nil, fmt.Errorf("解析幂等记录: %w", err)
	}
	return record, nil
}

/* Put 保存响应快照并设置与业务一致的 TTL */
func (s *IdempotencyStore) Put(ctx context.Context, scope, keyName string, record *port.IdempotencyRecord, ttl time.Duration) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("编码幂等记录: %w", err)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if err := s.client.rdb.Set(ctx, key("idem", scope, keyName), encoded, ttl).Err(); err != nil {
		return fmt.Errorf("写入幂等记录: %w", err)
	}
	return nil
}

/* Reserve 以 SETNX 写入占位记录；返回 false 表示同键请求已在处理 */
func (s *IdempotencyStore) Reserve(ctx context.Context, scope, keyName string,
	record *port.IdempotencyRecord, ttl time.Duration) (bool, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("编码幂等占位记录: %w", err)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	acquired, err := s.client.rdb.SetNX(ctx, key("idem", scope, keyName), encoded, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("写入幂等占位记录: %w", err)
	}
	return acquired, nil
}

/* releaseIdempotency 只在记录仍是同一请求的占位（Status=0）时删除 */
var releaseIdempotency = goredis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local record = cjson.decode(raw)
if record.Status == 0 and record.RequestHash == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

/* Release 释放占位；已完成快照与不同请求体的记录不受影响 */
func (s *IdempotencyStore) Release(ctx context.Context, scope, keyName, requestHash string) error {
	err := releaseIdempotency.Run(ctx, s.client.rdb, []string{key("idem", scope, keyName)}, requestHash).Err()
	if err != nil {
		return fmt.Errorf("释放幂等占位记录: %w", err)
	}
	return nil
}

/* AccessTokenDenylist 是 port.AccessTokenDenylist 的 Redis 实现，TTL 等于令牌剩余有效期 */
type AccessTokenDenylist struct {
	client *Client
}

/* NewAccessTokenDenylist 绑定共享客户端 */
func NewAccessTokenDenylist(client *Client) *AccessTokenDenylist {
	return &AccessTokenDenylist{client: client}
}

/* Add 写入已登出的令牌 ID，过期后自动清除 */
func (d *AccessTokenDenylist) Add(ctx context.Context, tokenID string, expiresAt time.Time) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil
	}
	if err := d.client.rdb.Set(ctx, key("deny", tokenID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("写入令牌黑名单: %w", err)
	}
	return nil
}

/* Contains 判断令牌是否已登出 */
func (d *AccessTokenDenylist) Contains(ctx context.Context, tokenID string) (bool, error) {
	exists, err := d.client.rdb.Exists(ctx, key("deny", tokenID)).Result()
	if err != nil {
		return false, fmt.Errorf("查询令牌黑名单: %w", err)
	}
	return exists > 0, nil
}

/* 确保实现端口契约 */
var (
	_ port.RateLimiter         = (*RateLimiter)(nil)
	_ port.FailureGuard        = (*FailureGuard)(nil)
	_ port.IdempotencyStore    = (*IdempotencyStore)(nil)
	_ port.AccessTokenDenylist = (*AccessTokenDenylist)(nil)
)
