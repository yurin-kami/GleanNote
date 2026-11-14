/*
 * 转换任务「未识别页码」的持久化往返：JSONB 写入、读取与空值归一。
 * 需要真实 PostgreSQL 16，未设置 GN_TEST_DATABASE_URL 时跳过。
 */
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* TestConversionJobUnreadablePagesRoundTrip 校验页码列表写入后可按原值读回 */
func TestConversionJobUnreadablePagesRoundTrip(t *testing.T) {
	db := testDB(t)
	repo := NewConversionJobRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	job := entity.NewConversionJob("job-unreadable", "note-1", "user-1",
		entity.JobKindCreate, 3, 3, 1, now)
	job.UnreadablePages = []int{2, 3}
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	stored, err := repo.GetByID(ctx, "user-1", "job-unreadable")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if len(stored.UnreadablePages) != 2 || stored.UnreadablePages[0] != 2 || stored.UnreadablePages[1] != 3 {
		t.Fatalf("未识别页 = %v，期望 [2 3]", stored.UnreadablePages)
	}

	/* 更新路径同样要覆盖该列：清空后读回应为空 */
	stored.UnreadablePages = nil
	applied, err := repo.Update(ctx, stored, entity.JobStateQueued)
	if err != nil || !applied {
		t.Fatalf("更新任务 = (%v, %v)，期望成功", applied, err)
	}
	cleared, err := repo.GetByID(ctx, "user-1", "job-unreadable")
	if err != nil {
		t.Fatalf("再次读取失败: %v", err)
	}
	if len(cleared.UnreadablePages) != 0 {
		t.Fatalf("清空后的未识别页 = %v，期望空", cleared.UnreadablePages)
	}
}
