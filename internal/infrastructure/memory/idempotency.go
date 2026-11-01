/* 内存幂等仓储：保存首次响应快照并按有效期回收 */
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* idempotencyEntry 是快照与到期时刻的组合 */
type idempotencyEntry struct {
	record    port.IdempotencyRecord
	expiresAt time.Time
}

/* IdempotencyStore 是 port.IdempotencyStore 的内存实现 */
type IdempotencyStore struct {
	mu      sync.Mutex
	clock   clock.Clock
	entries map[string]idempotencyEntry
}

/* NewIdempotencyStore 注入时钟，保证过期判定可测试 */
func NewIdempotencyStore(now clock.Clock) *IdempotencyStore {
	return &IdempotencyStore{clock: now, entries: make(map[string]idempotencyEntry)}
}

/* Get 返回未过期快照；未命中或已过期返回 nil, nil */
func (s *IdempotencyStore) Get(ctx context.Context, scope, key string) (*port.IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	composite := scope + "\x00" + key
	entry, exists := s.entries[composite]
	if !exists {
		return nil, nil
	}
	if !s.clock.Now().Before(entry.expiresAt) {
		delete(s.entries, composite)
		return nil, nil
	}
	copied := entry.record
	copied.Body = append([]byte(nil), entry.record.Body...)
	return &copied, nil
}

/* Put 保存响应快照，覆盖同一键的旧记录 */
func (s *IdempotencyStore) Put(ctx context.Context, scope, key string, record *port.IdempotencyRecord, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store(scope, key, record, ttl)
	return nil
}

/* Reserve 以 SETNX 语义写入占位；同键存在未过期记录时返回 false */
func (s *IdempotencyStore) Reserve(ctx context.Context, scope, key string, record *port.IdempotencyRecord, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	composite := scope + "\x00" + key
	if entry, exists := s.entries[composite]; exists && s.clock.Now().Before(entry.expiresAt) {
		return false, nil
	}
	s.store(scope, key, record, ttl)
	return true, nil
}

/* Release 只在记录仍是同一请求的占位时删除，已完成快照不受影响 */
func (s *IdempotencyStore) Release(ctx context.Context, scope, key, requestHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	composite := scope + "\x00" + key
	entry, exists := s.entries[composite]
	if !exists {
		return nil
	}
	if entry.record.Pending() && entry.record.RequestHash == requestHash {
		delete(s.entries, composite)
	}
	return nil
}

/* store 写入快照副本并刷新到期时刻；调用方必须持有互斥锁 */
func (s *IdempotencyStore) store(scope, key string, record *port.IdempotencyRecord, ttl time.Duration) {
	copied := *record
	copied.Body = append([]byte(nil), record.Body...)
	s.entries[scope+"\x00"+key] = idempotencyEntry{
		record:    copied,
		expiresAt: s.clock.Now().Add(ttl),
	}
}
