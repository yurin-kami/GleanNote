/* 内存窗口限流器：以滑动窗口计数模拟 Redis 令牌桶的开发态语义 */
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* RateLimiter 是 port.RateLimiter 的内存实现 */
type RateLimiter struct {
	mu      sync.Mutex
	clock   clock.Clock
	history map[string][]time.Time
}

/* NewRateLimiter 注入时钟以便测试窗口边界 */
func NewRateLimiter(now clock.Clock) *RateLimiter {
	return &RateLimiter{clock: now, history: make(map[string][]time.Time)}
}

/*
 * Take 在窗口内占用一次配额。
 * 返回 false 时给出「最早一次请求退出窗口」的剩余时间，最小 1 秒，供 Retry-After 使用。
 */
func (l *RateLimiter) Take(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if limit <= 0 || window <= 0 {
		return true, 0, nil
	}

	/* 先剔除窗口外记录，保持切片长度等于窗口内请求数 */
	kept := l.history[key][:0]
	for _, at := range l.history[key] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limit {
		l.history[key] = kept
		wait := kept[0].Add(window).Sub(now)
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait, nil
	}
	l.history[key] = append(kept, now)
	return true, 0, nil
}
