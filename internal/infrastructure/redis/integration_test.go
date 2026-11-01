/*
 * Redis 适配层集成测试（T-02）。
 *
 * 运行条件：设置 GN_TEST_REDIS_URL 指向可丢弃的 Redis 7 实例，
 * 未设置时全部用例跳过。每个用例前清理 gn: 前缀的业务键，避免污染共享实例。
 */
package redis

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* testClient 连接测试实例并清理业务键 */
func testClient(t *testing.T) *Client {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("GN_TEST_REDIS_URL"))
	if url == "" {
		t.Skip("未设置 GN_TEST_REDIS_URL，跳过 Redis 集成测试")
	}
	client, err := New(context.Background(), Options{URL: url})
	if err != nil {
		t.Fatalf("连接测试 Redis 失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	flushBusinessKeys(t, client)
	return client
}

/* flushBusinessKeys 逐个删除 gn: 前缀的键，不使用 FLUSHDB 以免影响共享实例 */
func flushBusinessKeys(t *testing.T, client *Client) {
	t.Helper()
	ctx := context.Background()
	iterator := client.rdb.Scan(ctx, 0, "gn:*", 100).Iterator()
	for iterator.Next(ctx) {
		if err := client.rdb.Del(ctx, iterator.Val()).Err(); err != nil {
			t.Fatalf("清理测试键失败: %v", err)
		}
	}
	if err := iterator.Err(); err != nil {
		t.Fatalf("遍历测试键失败: %v", err)
	}
}

/* fixClock 固定 nowFunc 并在用例结束后恢复 */
func fixClock(t *testing.T, fixed time.Time) {
	t.Helper()
	previous := nowFunc
	nowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { nowFunc = previous })
}

/* TestRateLimiterFixedWindow 验证固定窗口计数与窗口切换 */
func TestRateLimiterFixedWindow(t *testing.T) {
	client := testClient(t)
	limiter := NewRateLimiter(client)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fixClock(t, base)

	for i := 0; i < 3; i++ {
		allowed, _, err := limiter.Take(ctx, "login:1.1.1.1", 3, time.Minute)
		if err != nil {
			t.Fatalf("限流计数失败: %v", err)
		}
		if !allowed {
			t.Fatalf("第 %d 次请求不应超限", i+1)
		}
	}
	allowed, retryAfter, err := limiter.Take(ctx, "login:1.1.1.1", 3, time.Minute)
	if err != nil {
		t.Fatalf("超限计数失败: %v", err)
	}
	if allowed {
		t.Fatalf("第 4 次请求应被限流")
	}
	if retryAfter <= 0 {
		t.Fatalf("超限应返回正的重试间隔，实际 %v", retryAfter)
	}

	/* 进入下一个窗口后重新计数 */
	nowFunc = func() time.Time { return base.Add(61 * time.Second) }
	allowed, _, err = limiter.Take(ctx, "login:1.1.1.1", 3, time.Minute)
	if err != nil {
		t.Fatalf("新窗口计数失败: %v", err)
	}
	if !allowed {
		t.Fatalf("新窗口首个请求应放行")
	}
}

/* TestFailureGuardLocksAndClears 验证失败累计、锁定与成功清零 */
func TestFailureGuardLocksAndClears(t *testing.T) {
	client := testClient(t)
	guard := NewFailureGuard(client)
	ctx := context.Background()
	lockFor := time.Minute

	for i := 0; i < 2; i++ {
		locked, _, err := guard.Fail(ctx, "email:a@example.com", 2, lockFor)
		if err != nil {
			t.Fatalf("记录失败次数出错: %v", err)
		}
		if i == 0 && locked {
			t.Fatalf("首次失败不应锁定")
		}
		if i == 1 && !locked {
			t.Fatalf("达到阈值应锁定")
		}
	}

	locked, remaining, err := guard.Locked(ctx, "email:a@example.com")
	if err != nil {
		t.Fatalf("查询锁定状态失败: %v", err)
	}
	if !locked || remaining <= 0 {
		t.Fatalf("应处于锁定状态且剩余时间为正: locked=%v remaining=%v", locked, remaining)
	}

	if err := guard.Clear(ctx, "email:a@example.com"); err != nil {
		t.Fatalf("清除失败计数出错: %v", err)
	}
	locked, _, err = guard.Locked(ctx, "email:a@example.com")
	if err != nil {
		t.Fatalf("清除后查询锁定状态失败: %v", err)
	}
	if locked {
		t.Fatalf("清除后不应仍被锁定")
	}
}

/* TestIdempotencyReserveRelease 验证占位互斥与完成快照保护 */
func TestIdempotencyReserveRelease(t *testing.T) {
	client := testClient(t)
	store := NewIdempotencyStore(client)
	ctx := context.Background()

	acquired, err := store.Reserve(ctx, "note", "key-1",
		&port.IdempotencyRecord{RequestHash: "h1", Status: 0}, time.Minute)
	if err != nil {
		t.Fatalf("写入占位失败: %v", err)
	}
	if !acquired {
		t.Fatalf("首次占位应成功")
	}
	acquired, err = store.Reserve(ctx, "note", "key-1",
		&port.IdempotencyRecord{RequestHash: "h2", Status: 0}, time.Minute)
	if err != nil {
		t.Fatalf("重复占位出错: %v", err)
	}
	if acquired {
		t.Fatalf("同键并发占位不应成功")
	}

	record, err := store.Get(ctx, "note", "key-1")
	if err != nil {
		t.Fatalf("读取占位失败: %v", err)
	}
	if record == nil || record.RequestHash != "h1" || record.Status != 0 {
		t.Fatalf("占位内容错误: %+v", record)
	}

	/* 不同请求体的释放不得删除他人占位 */
	if err := store.Release(ctx, "note", "key-1", "h2"); err != nil {
		t.Fatalf("释放占位出错: %v", err)
	}
	if record, _ := store.Get(ctx, "note", "key-1"); record == nil {
		t.Fatalf("请求体不匹配时不应删除占位")
	}

	if err := store.Release(ctx, "note", "key-1", "h1"); err != nil {
		t.Fatalf("释放占位出错: %v", err)
	}
	if record, _ := store.Get(ctx, "note", "key-1"); record != nil {
		t.Fatalf("同请求体占位应被释放")
	}

	/* 已完成快照不得被释放 */
	completed := &port.IdempotencyRecord{RequestHash: "h1", Status: 201, Body: []byte(`{"id":"n1"}`)}
	if err := store.Put(ctx, "note", "key-1", completed, time.Minute); err != nil {
		t.Fatalf("写入完成快照失败: %v", err)
	}
	if err := store.Release(ctx, "note", "key-1", "h1"); err != nil {
		t.Fatalf("释放已完成快照出错: %v", err)
	}
	record, err = store.Get(ctx, "note", "key-1")
	if err != nil {
		t.Fatalf("读取完成快照失败: %v", err)
	}
	if record == nil || record.Status != 201 {
		t.Fatalf("完成快照不应被删除: %+v", record)
	}
}

/* TestAccessTokenDenylist 验证黑名单写入与过期语义 */
func TestAccessTokenDenylist(t *testing.T) {
	client := testClient(t)
	denylist := NewAccessTokenDenylist(client)
	ctx := context.Background()

	if err := denylist.Add(ctx, "token-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("写入黑名单失败: %v", err)
	}
	contains, err := denylist.Contains(ctx, "token-1")
	if err != nil {
		t.Fatalf("查询黑名单失败: %v", err)
	}
	if !contains {
		t.Fatalf("已登出的令牌应命中黑名单")
	}

	/* 已过期的令牌不再写入，避免无意义键 */
	if err := denylist.Add(ctx, "token-2", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("写入过期令牌失败: %v", err)
	}
	contains, err = denylist.Contains(ctx, "token-2")
	if err != nil {
		t.Fatalf("查询过期令牌失败: %v", err)
	}
	if contains {
		t.Fatalf("过期令牌不应进入黑名单")
	}
}

/* TestConversionQueueReliableDelivery 验证 FIFO、确认与在途恢复 */
func TestConversionQueueReliableDelivery(t *testing.T) {
	client := testClient(t)
	queue := NewConversionQueue(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, jobID := range []string{"job-1", "job-2"} {
		if err := queue.Enqueue(ctx, "user-1", jobID); err != nil {
			t.Fatalf("入队 %s 失败: %v", jobID, err)
		}
	}

	_, first, ok, err := queue.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("领取任务失败: ok=%v err=%v", ok, err)
	}
	if first != "job-1" {
		t.Fatalf("应先领取 job-1，实际 %s", first)
	}
	if err := queue.Ack(ctx, "user-1", first); err != nil {
		t.Fatalf("确认任务失败: %v", err)
	}
	if pending := client.rdb.LRange(ctx, key(processingKey), 0, -1).Val(); len(pending) != 0 {
		t.Fatalf("确认后在途集合应为空，实际 %v", pending)
	}

	_, second, ok, err := queue.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("领取第二个任务失败: ok=%v err=%v", ok, err)
	}
	if second != "job-2" {
		t.Fatalf("应领取 job-2，实际 %s", second)
	}
	/* 未确认即模拟进程退出，Recover 应把任务退回队列 */
	if err := queue.Recover(ctx); err != nil {
		t.Fatalf("恢复在途任务失败: %v", err)
	}
	if pending := client.rdb.LRange(ctx, key(processingKey), 0, -1).Val(); len(pending) != 0 {
		t.Fatalf("恢复后在途集合应为空，实际 %v", pending)
	}
	_, recovered, ok, err := queue.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("恢复后领取任务失败: ok=%v err=%v", ok, err)
	}
	if recovered != "job-2" {
		t.Fatalf("恢复后应重新领取 job-2，实际 %s", recovered)
	}
	if err := queue.Ack(ctx, "user-1", recovered); err != nil {
		t.Fatalf("确认恢复任务失败: %v", err)
	}
}
