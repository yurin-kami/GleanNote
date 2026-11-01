/* 内存访问令牌黑名单：按键保存到期时刻，过期键自动清理 */
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* AccessTokenDenylist 是 port.AccessTokenDenylist 的内存实现 */
type AccessTokenDenylist struct {
	mu      sync.Mutex
	clock   clock.Clock
	expires map[string]time.Time
}

/* NewAccessTokenDenylist 注入时钟，便于测试验证过期清理 */
func NewAccessTokenDenylist(now clock.Clock) *AccessTokenDenylist {
	return &AccessTokenDenylist{clock: now, expires: make(map[string]time.Time)}
}

/* Add 记录已吊销的 jti；到期时刻不晚于当前时间的记录直接忽略 */
func (d *AccessTokenDenylist) Add(ctx context.Context, tokenID string, expiresAt time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.clock.Now().Before(expiresAt) {
		return nil
	}
	d.expires[tokenID] = expiresAt
	return nil
}

/* Contains 判断 jti 是否仍被拒绝；读取时顺手删除过期项，避免无界增长 */
func (d *AccessTokenDenylist) Contains(ctx context.Context, tokenID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	expiresAt, exists := d.expires[tokenID]
	if !exists {
		return false, nil
	}
	if !d.clock.Now().Before(expiresAt) {
		delete(d.expires, tokenID)
		return false, nil
	}
	return true, nil
}
