/* 内存额度账本与容量计量：以 (userID, jobID) 与 (userID, 日期) 为幂等键 */
package memory

import (
	"context"
	"fmt"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* chargeEntry 记录一次扣减的日期与数量，返还时据此回滚当天余额 */
type chargeEntry struct {
	quotaDate string
	amount    int
}

/* QuotaLedger 是 port.QuotaLedger 的内存实现 */
type QuotaLedger struct {
	store    *Store
	charges  map[string]chargeEntry
	refunded map[string]struct{}
	used     map[string]int
}

/* NewQuotaLedger 绑定共享存储并初始化账本索引 */
func NewQuotaLedger(store *Store) *QuotaLedger {
	return &QuotaLedger{
		store:    store,
		charges:  make(map[string]chargeEntry),
		refunded: make(map[string]struct{}),
		used:     make(map[string]int),
	}
}

/* Remaining 返回指定北京时间日期的剩余额度；已用超出上限时返回 0 */
func (l *QuotaLedger) Remaining(ctx context.Context, userID, quotaDate string) (int, error) {
	release := l.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	remaining := valueobject.DailyPageQuota - l.used[usageKey(userID, quotaDate)]
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

/*
 * Charge 从当日额度扣减 amount。
 * 同一 jobID 重复扣减视为幂等成功；余额不足返回 QUOTA_EXCEEDED。
 */
func (l *QuotaLedger) Charge(ctx context.Context, userID, jobID, quotaDate string, amount, limit int) error {
	release := l.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return err
	}
	jobKey := ledgerKey(userID, jobID)
	if _, exists := l.charges[jobKey]; exists {
		return nil
	}
	usedKey := usageKey(userID, quotaDate)
	if l.used[usedKey]+amount > limit {
		return errs.New(errs.CodeQuotaExceeded, fmt.Sprintf("今日额度剩余 %d 张", limit-l.used[usedKey]))
	}
	previousUsed := l.used[usedKey]
	l.used[usedKey] = previousUsed + amount
	l.charges[jobKey] = chargeEntry{quotaDate: quotaDate, amount: amount}
	l.store.onRollback(ctx, func() {
		l.used[usedKey] = previousUsed
		delete(l.charges, jobKey)
	})
	return nil
}

/*
 * Refund 依据原扣减流水返还额度。
 * 同一 jobID 重复返还是幂等成功；找不到扣减记录时静默返回，避免补偿任务失败。
 */
func (l *QuotaLedger) Refund(ctx context.Context, userID, jobID string, amount int) error {
	release := l.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return err
	}
	jobKey := ledgerKey(userID, jobID)
	charge, exists := l.charges[jobKey]
	if !exists {
		return nil
	}
	if _, refunded := l.refunded[jobKey]; refunded {
		return nil
	}
	usedKey := usageKey(userID, charge.quotaDate)
	previousUsed := l.used[usedKey]
	l.used[usedKey] = previousUsed - charge.amount
	if l.used[usedKey] < 0 {
		l.used[usedKey] = 0
	}
	l.refunded[jobKey] = struct{}{}
	l.store.onRollback(ctx, func() {
		l.used[usedKey] = previousUsed
		delete(l.refunded, jobKey)
	})
	return nil
}

/* StorageUsage 是 port.StorageUsage 的内存实现 */
type StorageUsage struct {
	store *Store
	used  map[string]int64
}

/* NewStorageUsage 绑定共享存储并初始化按用户的用量表 */
func NewStorageUsage(store *Store) *StorageUsage {
	return &StorageUsage{
		store: store,
		used:  make(map[string]int64),
	}
}

/* Used 返回用户已占用的原图字节数 */
func (u *StorageUsage) Used(ctx context.Context, userID string) (int64, error) {
	release := u.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return u.used[userID], nil
}

/* Reserve 预占容量；超过 limit 返回 STORAGE_EXCEEDED */
func (u *StorageUsage) Reserve(ctx context.Context, userID string, bytes, limit int64) error {
	release := u.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return err
	}
	previous := u.used[userID]
	if previous+bytes > limit {
		return errs.New(errs.CodeStorageExceeded, "云端空间不足")
	}
	u.used[userID] = previous + bytes
	u.store.onRollback(ctx, func() { u.used[userID] = previous })
	return nil
}

/* Release 释放容量；不会把已用量减为负数，避免并发重复释放导致漂移 */
func (u *StorageUsage) Release(ctx context.Context, userID string, bytes int64) error {
	release := u.store.lock(ctx)
	defer release()

	if err := ctx.Err(); err != nil {
		return err
	}
	previous := u.used[userID]
	next := previous - bytes
	if next < 0 {
		next = 0
	}
	if next == previous {
		return nil
	}
	u.used[userID] = next
	u.store.onRollback(ctx, func() { u.used[userID] = previous })
	return nil
}

/* ledgerKey 以用户与任务组合幂等键，任务 ID 全局唯一但显式带上属主便于审计 */
func ledgerKey(userID, jobID string) string {
	return userID + "/" + jobID
}

/* usageKey 以用户与日期组合当日额度用量键 */
func usageKey(userID, quotaDate string) string {
	return userID + "/" + quotaDate
}
