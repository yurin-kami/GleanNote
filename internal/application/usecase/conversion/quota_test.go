/* 额度总览测试：当日额度、重置时间、转换进行中标记与云端容量 */
package conversion

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/* TestQuotaOverviewReportsDailyAndStorage 校验额度口径与容量占比 */
func TestQuotaOverviewReportsDailyAndStorage(t *testing.T) {
	f := newFixture(t)
	userID := "user-quota"

	view, err := f.service.QuotaOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("读取额度: %v", err)
	}
	if view.QuotaDate != "2026-10-28" || view.DailyLimit != 50 || view.Remaining != 50 {
		t.Fatalf("额度字段不符: %+v", view)
	}
	if !view.ResetAt.Equal(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("重置时间 = %s，期望北京时间次日 0 点", view.ResetAt)
	}
	if view.ConversionRunning {
		t.Fatalf("没有任务时 conversionRunning 应为 false")
	}
	if view.Storage.UsedBytes != 0 || view.Storage.LimitBytes != 200*1024*1024 || view.Storage.UsedPercent != 0 {
		t.Fatalf("容量字段不符: %+v", view.Storage)
	}

	now := f.clock.Now()
	if err := f.ledger.Charge(context.Background(), userID, "job-quota", bizdate.Day(now), 10, 50); err != nil {
		t.Fatalf("扣减额度: %v", err)
	}
	if err := f.usage.Reserve(context.Background(), userID, 20*1024*1024, 200*1024*1024); err != nil {
		t.Fatalf("预占容量: %v", err)
	}
	if err := f.jobs.Create(context.Background(), entity.NewConversionJob(
		"job-quota", "note-quota", userID, entity.JobKindCreate, 10, 10, 1, now)); err != nil {
		t.Fatalf("写入任务: %v", err)
	}

	view, err = f.service.QuotaOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("读取额度: %v", err)
	}
	if view.Remaining != 40 {
		t.Fatalf("剩余额度 = %d，期望 40", view.Remaining)
	}
	if !view.ConversionRunning {
		t.Fatalf("存在排队任务时 conversionRunning 应为 true")
	}
	if view.Storage.UsedBytes != 20*1024*1024 || view.Storage.UsedPercent != 10 {
		t.Fatalf("容量字段不符: %+v", view.Storage)
	}
}
