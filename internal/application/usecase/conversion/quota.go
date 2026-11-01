/* 额度与云端容量总览用例：供设置页与转换入口展示 */
package conversion

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/* StorageView 是云端容量用量，百分比按用量向上取整 */
type StorageView struct {
	UsedBytes   int64
	LimitBytes  int64
	UsedPercent int
}

/* QuotaOverviewView 对应 docs/API.md#517-用户与额度 的响应结构 */
type QuotaOverviewView struct {
	QuotaDate         string
	DailyLimit        int
	Remaining         int
	ResetAt           time.Time
	ConversionRunning bool
	Storage           StorageView
}

/*
 * QuotaOverview 汇总当日剩余额度、重置时间、是否仍有转换进行中与云端容量。
 * 口径：日期按北京时间结算，重置点为次日 0 点（bizdate.ResetAt）；
 * 容量按原图字节计量，回收站内容继续占用。不消耗额度、不产生写入。
 * 参考：docs/API.md#517-用户与额度、PRD FR-11
 */
func (s *Service) QuotaOverview(ctx context.Context, userID string) (QuotaOverviewView, error) {
	if err := requireUser(userID); err != nil {
		return QuotaOverviewView{}, err
	}

	now := s.clock.Now()
	quotaDate := bizdate.Day(now)
	remaining, err := s.deps.Quota.Remaining(ctx, userID, quotaDate)
	if err != nil {
		return QuotaOverviewView{}, err
	}
	used, err := s.deps.Storage.Used(ctx, userID)
	if err != nil {
		return QuotaOverviewView{}, err
	}
	active, err := s.deps.Jobs.ListActiveByUser(ctx, userID)
	if err != nil {
		return QuotaOverviewView{}, err
	}
	return QuotaOverviewView{
		QuotaDate:         quotaDate,
		DailyLimit:        valueobject.DailyPageQuota,
		Remaining:         remaining,
		ResetAt:           bizdate.ResetAt(now),
		ConversionRunning: len(active) > 0,
		Storage: StorageView{
			UsedBytes:   used,
			LimitBytes:  valueobject.StorageLimitBytes,
			UsedPercent: valueobject.UsedPercent(used, valueobject.StorageLimitBytes),
		},
	}, nil
}
