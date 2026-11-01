/* 测试内存限流、失败锁定与幂等快照的时间边界和并发安全 */
package memory

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* 限流在窗口内只允许 limit 次，窗口滑过后恢复 */
func TestRateLimiterWindow(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC))
	limiter := NewRateLimiter(fixed)
	ctx := context.Background()

	for index := 0; index < 2; index++ {
		allowed, _, err := limiter.Take(ctx, "k", 2, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("第 %d 次应放行: %v %v", index, allowed, err)
		}
	}
	allowed, wait, err := limiter.Take(ctx, "k", 2, time.Minute)
	if err != nil || allowed {
		t.Fatalf("第 3 次应拒绝: %v %v", allowed, err)
	}
	if wait < time.Second || wait > time.Minute {
		t.Fatalf("重试间隔不合理: %s", wait)
	}

	fixed.Advance(time.Minute)
	if allowed, _, _ := limiter.Take(ctx, "k", 2, time.Minute); !allowed {
		t.Fatal("窗口滑过后应恢复")
	}
}

/* 连续失败达到阈值后锁定，锁定到期自动解除，Clear 立即清零 */
func TestFailureGuardLock(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC))
	guard := NewFailureGuard(fixed)
	ctx := context.Background()

	for index := 0; index < 4; index++ {
		locked, _, _ := guard.Fail(ctx, "acct", 5, 15*time.Minute)
		if locked {
			t.Fatalf("第 %d 次不应锁定", index+1)
		}
	}
	locked, wait, _ := guard.Fail(ctx, "acct", 5, 15*time.Minute)
	if !locked || wait != 15*time.Minute {
		t.Fatalf("第 5 次应锁定 15 分钟: %v %s", locked, wait)
	}
	if locked, _, _ := guard.Locked(ctx, "acct"); !locked {
		t.Fatal("锁定期内应拒绝登录")
	}

	fixed.Advance(15 * time.Minute)
	if locked, _, _ := guard.Locked(ctx, "acct"); locked {
		t.Fatal("锁定到期后应解除")
	}

	guard.Fail(ctx, "acct", 5, 15*time.Minute)
	if err := guard.Clear(ctx, "acct"); err != nil {
		t.Fatalf("清零失败: %v", err)
	}
	if locked, _, _ := guard.Locked(ctx, "acct"); locked {
		t.Fatal("Clear 后不应锁定")
	}
}

/* 幂等快照读取必须返回副本，且过期后不再命中 */
func TestIdempotencyStore(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC))
	store := NewIdempotencyStore(fixed)
	ctx := context.Background()

	record := &port.IdempotencyRecord{RequestHash: "h", Status: 201, Body: []byte(`{"ok":true}`)}
	if err := store.Put(ctx, "auth:register", "key-1", record, 24*time.Hour); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := store.Get(ctx, "auth:register", "key-1")
	if err != nil || got == nil {
		t.Fatalf("应命中快照: %v", err)
	}
	got.Body[0] = 'x'
	again, _ := store.Get(ctx, "auth:register", "key-1")
	if string(again.Body) != `{"ok":true}` {
		t.Fatal("快照应返回副本")
	}

	if miss, _ := store.Get(ctx, "auth:login", "key-1"); miss != nil {
		t.Fatal("不同作用域不应命中")
	}

	fixed.Advance(25 * time.Hour)
	if expired, _ := store.Get(ctx, "auth:register", "key-1"); expired != nil {
		t.Fatal("过期后不应命中")
	}
}
