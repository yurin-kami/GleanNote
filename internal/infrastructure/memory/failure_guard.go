/* 内存失败守卫：统计连续失败次数并在达到阈值后锁定一段时间 */
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* failureState 记录连续失败次数与锁定截止时刻 */
type failureState struct {
	count       int
	lockedUntil time.Time
}

/* FailureGuard 是 port.FailureGuard 的内存实现 */
type FailureGuard struct {
	mu     sync.Mutex
	clock  clock.Clock
	states map[string]*failureState
}

/* NewFailureGuard 注入时钟以验证锁定时长 */
func NewFailureGuard(now clock.Clock) *FailureGuard {
	return &FailureGuard{clock: now, states: make(map[string]*failureState)}
}

/* Locked 返回是否仍在锁定期以及剩余秒数；锁定过期后自动清空计数 */
func (g *FailureGuard) Locked(ctx context.Context, key string) (bool, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, exists := g.states[key]
	if !exists {
		return false, 0, nil
	}
	remaining := state.lockedUntil.Sub(g.clock.Now())
	if remaining > 0 {
		return true, remaining, nil
	}
	if !state.lockedUntil.IsZero() {
		delete(g.states, key)
	}
	return false, 0, nil
}

/* Fail 记录一次失败；达到 max 时锁定 lockFor 并返回剩余锁定时间 */
func (g *FailureGuard) Fail(ctx context.Context, key string, max int, lockFor time.Duration) (bool, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, exists := g.states[key]
	if !exists {
		state = &failureState{}
		g.states[key] = state
	}
	state.count++
	if state.count < max {
		return false, 0, nil
	}
	state.count = 0
	state.lockedUntil = g.clock.Now().Add(lockFor)
	return true, lockFor, nil
}

/* Clear 在认证成功后清空失败计数 */
func (g *FailureGuard) Clear(ctx context.Context, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.states, key)
	return nil
}
