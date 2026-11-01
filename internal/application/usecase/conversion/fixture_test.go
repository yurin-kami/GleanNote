/* 转换用例测试夹具：内存仓储与队列 + 可注入供应商 + 固定时钟 */
package conversion

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* sequenceIDs 生成可读的自增 ID，便于断言 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* stubVision 是可控供应商，用于注入成功结果与各类失败 */
type stubVision struct {
	result *port.VisionResult
	err    error
}

/* Name 返回固定名称 */
func (v *stubVision) Name() string { return "stub" }

/* Convert 返回预置结果或错误，不访问网络 */
func (v *stubVision) Convert(ctx context.Context, pages []port.VisionPage) (*port.VisionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.result, v.err
}

/* Health 始终可用，避免探针干扰用例 */
func (v *stubVision) Health(ctx context.Context) error { return nil }

/* stubVersions 记录转换成功后写入的版本来源，避免在转换用例内依赖完整版本服务 */
type stubVersions struct{ sources []string }

/* Record 记录来源并立即成功 */
func (s *stubVersions) Record(ctx context.Context, userID, noteID, source, content string) error {
	s.sources = append(s.sources, source)
	return nil
}

/* fixture 汇总转换用例测试所需的依赖 */
type fixture struct {
	service  *Service
	clock    *clock.Fixed
	store    *memory.Store
	notes    *memory.NoteRepository
	pages    *memory.NotePageRepository
	jobs     *memory.ConversionJobRepository
	ledger   *memory.QuotaLedger
	usage    *memory.StorageUsage
	queue    *memory.ConversionQueue
	objects  *objectstore.Fake
	ids      *sequenceIDs
	vision   *stubVision
	versions *stubVersions
}

/* newFixture 装配转换服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	notes := memory.NewNoteRepository(store)
	pages := memory.NewNotePageRepository(store)
	jobs := memory.NewConversionJobRepository(store)
	ledger := memory.NewQuotaLedger(store)
	usage := memory.NewStorageUsage(store)
	queue := memory.NewConversionQueue(16)
	objects := objectstore.NewFake(fixed)
	stub := &stubVision{}
	versions := &stubVersions{}
	ids := &sequenceIDs{next: 1000}
	svc, err := NewService(Deps{
		Clock:        fixed,
		IDs:          ids,
		Notes:        notes,
		Pages:        pages,
		Jobs:         jobs,
		Quota:        ledger,
		Storage:      usage,
		Queue:        queue,
		Vision:       stub,
		Objects:      objects,
		Versions:     versions,
		Transactions: store,
	})
	if err != nil {
		t.Fatalf("创建转换服务: %v", err)
	}
	return &fixture{
		service:  svc,
		clock:    fixed,
		store:    store,
		notes:    notes,
		pages:    pages,
		jobs:     jobs,
		ledger:   ledger,
		usage:    usage,
		queue:    queue,
		objects:  objects,
		ids:      ids,
		vision:   stub,
		versions: versions,
	}
}

/* seedDraft 写入一篇 DRAFT 笔记并追加指定数量的页面 */
func (f *fixture) seedDraft(t *testing.T, noteID, userID string, pageCount int) {
	t.Helper()
	title, err := valueobject.ParseNoteTitle("")
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	note := entity.NewDraftNote(noteID, userID, "folder-1", title, f.clock.Now())
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	pages := make([]*entity.NotePage, 0, pageCount)
	for index := 1; index <= pageCount; index++ {
		upload := entity.NewUpload(fmt.Sprintf("up-%d-%d", index, len(noteID)), userID,
			fmt.Sprintf("u/up-%d.jpg", index), "p.jpg", "image/jpeg", strings.Repeat("a", 64),
			2048, f.clock.Now().Add(time.Hour), f.clock.Now())
		upload.MarkReady(800, 600, fmt.Sprintf("thumb/up-%d.jpg", index), "")
		pages = append(pages, entity.NewNotePage(fmt.Sprintf("%s-page-%d", noteID, index), noteID, userID,
			index, upload, f.clock.Now()))
	}
	if len(pages) > 0 {
		if err := f.pages.CreateBatch(context.Background(), pages); err != nil {
			t.Fatalf("写入页面: %v", err)
		}
	}
}

/* seedJob 写入排队中的任务并扣减额度，模拟提交流程已完成计费 */
func (f *fixture) seedJob(t *testing.T, jobID, noteID, userID string, pages int, state entity.JobState) *entity.ConversionJob {
	t.Helper()
	now := f.clock.Now()
	job := entity.NewConversionJob(jobID, noteID, userID, entity.JobKindCreate, pages, pages, 1, now)
	if err := f.jobs.Create(context.Background(), job); err != nil {
		t.Fatalf("写入任务: %v", err)
	}
	if err := f.ledger.Charge(context.Background(), userID, jobID, bizdate.Day(now), pages, valueobject.DailyPageQuota); err != nil {
		t.Fatalf("扣减额度: %v", err)
	}
	if state != entity.JobStateQueued {
		stored, _ := f.jobs.GetByID(context.Background(), userID, jobID)
		switch state {
		case entity.JobStateFailed:
			stored.Fail(entity.JobFailureProviderUnavailable, now)
		case entity.JobStateCanceled:
			if err := stored.Cancel(now); err != nil {
				t.Fatalf("取消任务: %v", err)
			}
		}
		if _, err := f.jobs.Update(context.Background(), stored, entity.JobStateQueued); err != nil {
			t.Fatalf("更新任务: %v", err)
		}
		/* 失败与取消在真实流程中已返还额度，夹具保持一致，避免重试用例误判余额 */
		if state == entity.JobStateFailed || state == entity.JobStateCanceled {
			if err := f.ledger.Refund(context.Background(), userID, jobID, pages); err != nil {
				t.Fatalf("返还额度: %v", err)
			}
		}
	}
	return job
}

/* remaining 读取当日剩余额度 */
func (f *fixture) remaining(t *testing.T, userID string) int {
	t.Helper()
	value, err := f.ledger.Remaining(context.Background(), userID, bizdate.Day(f.clock.Now()))
	if err != nil {
		t.Fatalf("读取余额: %v", err)
	}
	return value
}

/* nextQueued 以短超时读取队列中的下一个任务 ID */
func (f *fixture) nextQueued(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, jobID, ok, err := f.queue.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("读取队列失败: ok=%v err=%v", ok, err)
	}
	return jobID
}
