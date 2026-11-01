/* 测试内存仓储、额度账本、容量计量与任务队列的行为与回滚语义 */
package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* baseTime 固定测试时间，避免用例依赖真实时钟 */
var baseTime = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

/* codeOf 提取业务错误码；非业务错误返回空值，便于断言 */
func codeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	return code
}

/* newUpload 构造一个状态为 READY 的上传会话，便于页面与任务测试复用 */
func newUpload(id, userID string) *entity.Upload {
	upload := entity.NewUpload(id, userID, "uploads/"+id+".jpg", id+".jpg",
		"image/jpeg", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		1024, baseTime.Add(30*time.Minute), baseTime)
	upload.MarkReady(800, 1200, "thumb/"+id+".jpg", "")
	return upload
}

/* TestUploadRepositoryBatchAndUpdate 校验批量写入、属主隔离与更新 */
func TestUploadRepositoryBatchAndUpdate(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	repo := NewUploadRepository(store)

	if err := repo.CreateBatch(ctx, []*entity.Upload{newUpload("u1", "user-a")}); err != nil {
		t.Fatalf("批量写入失败: %v", err)
	}
	got, err := repo.GetByID(ctx, "user-a", "u1")
	if err != nil || got.ID != "u1" || !got.IsReady() {
		t.Fatalf("读取失败: %v %+v", err, got)
	}
	if _, err := repo.GetByID(ctx, "user-b", "u1"); codeOf(err) != errs.CodeUploadNotFound {
		t.Fatalf("跨用户读取应返回 UPLOAD_NOT_FOUND，实际 %v", err)
	}

	got.MarkExpired()
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	reloaded, _ := repo.GetByID(ctx, "user-a", "u1")
	if reloaded.Status != entity.UploadStatusExpired {
		t.Fatalf("状态 = %q，期望 EXPIRED", reloaded.Status)
	}
}

/* TestNotePageRepositoryPagination 校验排序、游标分页与统计 */
func TestNotePageRepositoryPagination(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	repo := NewNotePageRepository(store)

	pages := []*entity.NotePage{
		entity.NewNotePage("p3", "note-1", "user-a", 3, newUpload("u3", "user-a"), baseTime),
		entity.NewNotePage("p1", "note-1", "user-a", 1, newUpload("u1", "user-a"), baseTime),
		entity.NewNotePage("p2", "note-1", "user-a", 2, newUpload("u2", "user-a"), baseTime),
		entity.NewNotePage("q1", "note-2", "user-a", 1, newUpload("u4", "user-a"), baseTime),
	}
	if err := repo.CreateBatch(ctx, pages); err != nil {
		t.Fatalf("写入页面失败: %v", err)
	}

	first, err := repo.List(ctx, port.NotePageQuery{UserID: "user-a", NoteID: "note-1", Limit: 2})
	if err != nil || len(first) != 2 || first[0].ID != "p1" || first[1].ID != "p2" {
		t.Fatalf("首页 = %+v，err=%v", first, err)
	}
	second, err := repo.List(ctx, port.NotePageQuery{
		UserID: "user-a", NoteID: "note-1", Limit: 2,
		After: &port.NotePageCursor{Index: first[1].Index, ID: first[1].ID},
	})
	if err != nil || len(second) != 1 || second[0].ID != "p3" {
		t.Fatalf("游标页 = %+v，err=%v", second, err)
	}

	counts, err := repo.CountByNotes(ctx, "user-a", []string{"note-1", "note-2", "note-3"})
	if err != nil || counts["note-1"] != 3 || counts["note-2"] != 1 || counts["note-3"] != 0 {
		t.Fatalf("批量统计 = %+v，err=%v", counts, err)
	}
	firstPages, err := repo.FirstByNotes(ctx, "user-a", []string{"note-1"})
	if err != nil || firstPages["note-1"] == nil || firstPages["note-1"].ID != "p1" {
		t.Fatalf("首图 = %+v，err=%v", firstPages, err)
	}

	deleted, err := repo.DeleteByNote(ctx, "user-a", "note-1")
	if err != nil || len(deleted) != 3 {
		t.Fatalf("删除页面 = %d，err=%v", len(deleted), err)
	}
	if count, _ := repo.CountByNote(ctx, "user-a", "note-1"); count != 0 {
		t.Fatalf("删除后计数 = %d，期望 0", count)
	}
}

/* TestNotePageRepositoryRollback 校验事务失败时页面写入被撤销 */
func TestNotePageRepositoryRollback(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	repo := NewNotePageRepository(store)

	sentinel := errors.New("boom")
	err := store.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := repo.CreateBatch(txCtx, []*entity.NotePage{
			entity.NewNotePage("p1", "note-1", "user-a", 1, newUpload("u1", "user-a"), baseTime),
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("事务错误 = %v，期望 sentinel", err)
	}
	if count, _ := repo.CountByNote(ctx, "user-a", "note-1"); count != 0 {
		t.Fatalf("回滚后页面数 = %d，期望 0", count)
	}
}

/* TestConversionJobRepositoryLatest 校验最近任务、更新与按笔记删除 */
func TestConversionJobRepositoryLatest(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	repo := NewConversionJobRepository(store)

	if _, err := repo.GetLatestByNote(ctx, "user-a", "note-1"); codeOf(err) != errs.CodeConversionNotFound {
		t.Fatalf("无任务应返回 CONVERSION_NOT_FOUND，实际 %v", err)
	}

	first := entity.NewConversionJob("j1", "note-1", "user-a", entity.JobKindCreate, 3, 3, 1, baseTime)
	second := entity.NewConversionJob("j2", "note-1", "user-a", entity.JobKindCreate, 3, 3, 2, baseTime.Add(time.Minute))
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	latest, err := repo.GetLatestByNote(ctx, "user-a", "note-1")
	if err != nil || latest.ID != "j2" {
		t.Fatalf("最近任务 = %+v，err=%v", latest, err)
	}

	latest.Start(baseTime.Add(2 * time.Minute))
	if _, err := repo.Update(ctx, latest, entity.JobStateQueued); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	reloaded, _ := repo.GetByID(ctx, "user-a", "j2")
	if reloaded.State != entity.JobStateRunning || reloaded.StartedAt == nil {
		t.Fatalf("任务状态 = %q，开始时间 = %v", reloaded.State, reloaded.StartedAt)
	}

	if err := repo.DeleteByNote(ctx, "user-a", "note-1"); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if _, err := repo.GetByID(ctx, "user-a", "j1"); codeOf(err) != errs.CodeConversionNotFound {
		t.Fatalf("删除后读取应失败，实际 %v", err)
	}
}

/* TestQuotaLedgerChargeRefund 校验扣减、余额不足、幂等返还与日期隔离 */
func TestQuotaLedgerChargeRefund(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	ledger := NewQuotaLedger(store)

	if remaining, _ := ledger.Remaining(ctx, "user-a", "2026-10-28"); remaining != valueobject.DailyPageQuota {
		t.Fatalf("初始余额 = %d，期望 %d", remaining, valueobject.DailyPageQuota)
	}
	if err := ledger.Charge(ctx, "user-a", "j1", "2026-10-28", 20, valueobject.DailyPageQuota); err != nil {
		t.Fatalf("扣减失败: %v", err)
	}
	if err := ledger.Charge(ctx, "user-a", "j1", "2026-10-28", 20, valueobject.DailyPageQuota); err != nil {
		t.Fatalf("重复扣减应幂等: %v", err)
	}
	if remaining, _ := ledger.Remaining(ctx, "user-a", "2026-10-28"); remaining != 30 {
		t.Fatalf("余额 = %d，期望 30", remaining)
	}
	if err := ledger.Charge(ctx, "user-a", "j2", "2026-10-28", 40, valueobject.DailyPageQuota); codeOf(err) != errs.CodeQuotaExceeded {
		t.Fatalf("超额扣减应返回 QUOTA_EXCEEDED，实际 %v", err)
	}

	if err := ledger.Refund(ctx, "user-a", "j1", 20); err != nil {
		t.Fatalf("返还失败: %v", err)
	}
	if err := ledger.Refund(ctx, "user-a", "j1", 20); err != nil {
		t.Fatalf("重复返还应幂等: %v", err)
	}
	if remaining, _ := ledger.Remaining(ctx, "user-a", "2026-10-28"); remaining != valueobject.DailyPageQuota {
		t.Fatalf("返还后余额 = %d，期望 %d", remaining, valueobject.DailyPageQuota)
	}
	if remaining, _ := ledger.Remaining(ctx, "user-a", "2026-10-29"); remaining != valueobject.DailyPageQuota {
		t.Fatalf("次日余额 = %d，期望独立计数", remaining)
	}
}

/* TestStorageUsageReserveRelease 校验容量预占、超限与释放下限 */
func TestStorageUsageReserveRelease(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	usage := NewStorageUsage(store)
	limit := int64(100)

	if err := usage.Reserve(ctx, "user-a", 60, limit); err != nil {
		t.Fatalf("预占失败: %v", err)
	}
	if err := usage.Reserve(ctx, "user-a", 50, limit); codeOf(err) != errs.CodeStorageExceeded {
		t.Fatalf("超额预占应返回 STORAGE_EXCEEDED，实际 %v", err)
	}
	if used, _ := usage.Used(ctx, "user-a"); used != 60 {
		t.Fatalf("用量 = %d，期望 60", used)
	}

	if err := usage.Release(ctx, "user-a", 100); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if used, _ := usage.Used(ctx, "user-a"); used != 0 {
		t.Fatalf("释放后用量 = %d，期望 0", used)
	}
}

/* TestConversionQueueOrderAndClose 校验 FIFO 顺序与关闭后入队错误 */
func TestConversionQueueOrderAndClose(t *testing.T) {
	ctx := context.Background()
	queue := NewConversionQueue(2)

	if err := queue.Enqueue(ctx, "user-a", "j1"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if err := queue.Enqueue(ctx, "user-b", "j2"); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	for _, expected := range []string{"j1", "j2"} {
		_, jobID, ok, err := queue.Next(ctx)
		if err != nil || !ok || jobID != expected {
			t.Fatalf("出队 = %q ok=%v err=%v，期望 %q", jobID, ok, err, expected)
		}
	}

	queue.Close()
	if err := queue.Enqueue(ctx, "user-a", "j3"); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("关闭后入队应返回 ErrQueueClosed，实际 %v", err)
	}
	if _, _, ok, err := queue.Next(ctx); ok || err != nil {
		t.Fatalf("关闭后出队应返回 ok=false 且无错误，实际 ok=%v err=%v", ok, err)
	}
}
