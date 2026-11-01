/*
 * Package memory 提供进程内实现，供开发态运行与单元测试使用。
 *
 * 职责：以同一把互斥锁串行化写入，并用撤销日志模拟事务回滚；
 * 数据在进程重启后丢失，不能作为生产存储。
 * 不负责：SQL、索引、跨进程一致性；生产持久化由 infrastructure/postgres 承担。
 * 依赖：domain 实体与端口定义、platform/clock。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/CONVENTIONS.md#33-测试 T-02
 */
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* ctxKey 用来判断当前上下文是否处于事务中，避免仓储重复加锁 */
type ctxKey struct{}

/* nowFunc 统一仓储取时入口，便于测试替换；生产固定返回 UTC 系统时间（G-09） */
var nowFunc = func() time.Time { return time.Now().UTC() }

/* txState 记录事务内的撤销动作，逆序执行即可回到事务前状态 */
type txState struct {
	undo []func()
}

/*
 * Store 是所有内存仓储共享的并发控制与事务边界。
 * 约束：不支持嵌套事务，事务内调用仓储方法时不会再次加锁。
 */
type Store struct {
	mu      sync.Mutex
	changes *ChangeLogRepository
}

/* NewStore 创建空存储；所有仓储必须复用同一实例才能共享事务语义 */
func NewStore() *Store {
	return &Store{}
}

/*
 * WithinTransaction 在独占锁内执行 fn。
 * fn 返回错误时按写入逆序回滚；仅模拟单进程原子的读写，不提供隔离级别。
 */
func (s *Store) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := &txState{}
	if err := fn(context.WithValue(ctx, ctxKey{}, state)); err != nil {
		for index := len(state.undo) - 1; index >= 0; index-- {
			state.undo[index]()
		}
		return err
	}
	return nil
}

/* lock 在事务外获取互斥锁；事务内直接复用外层锁，防止自死锁 */
func (s *Store) lock(ctx context.Context) func() {
	if stateFrom(ctx) != nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

/* onRollback 把撤销动作登记到当前事务；事务外调用时忽略 */
func (s *Store) onRollback(ctx context.Context, undo func()) {
	if state := stateFrom(ctx); state != nil {
		state.undo = append(state.undo, undo)
	}
}

/*
 * SetChangeLog 绑定变更日志仓储。
 * 约定：各仓储在写事务内调用 recordChange，等价于生产库的触发器/CDC；
 * 未绑定时写入照常进行，仅不产生同步事件（测试夹具可按需注入）。
 */
func (s *Store) SetChangeLog(log *ChangeLogRepository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = log
}

/* recordChange 在同一事务内追加变更事件；未绑定日志时静默跳过 */
func (s *Store) recordChange(ctx context.Context, event *entity.ChangeEvent) {
	if s.changes == nil {
		return
	}
	/* 调用方已持有存储锁或处于事务内，这里必须复用不加锁的追加路径 */
	_ = s.changes.appendLocked(ctx, event)
}

/* stateFrom 从上下文读取事务状态，事务外返回 nil */
func stateFrom(ctx context.Context) *txState {
	state, _ := ctx.Value(ctxKey{}).(*txState)
	return state
}
