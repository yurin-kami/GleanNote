/* 测试任务执行、失败返还、取消与重试 */
package conversion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* codeOf 提取业务错误码，便于断言 */
func codeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	return code
}

/* TestRunJobSuccess 校验写正文、标题回落、任务成功与额度不返还 */
func TestRunJobSuccess(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	f.vision.result = &port.VisionResult{
		Markdown: "## 第 1 页\n\n示例内容",
		Provider: "stub",
	}

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行任务失败: %v", err)
	}
	job, err := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if err != nil || job.State != entity.JobStateSucceeded || job.Phase != entity.JobPhaseCompleted {
		t.Fatalf("任务 = %+v，err=%v", job, err)
	}
	if job.ProcessedPages != 1 || job.FinishedAt == nil {
		t.Fatalf("进度或结束时间缺失: %+v", job)
	}

	note, err := f.notes.GetByID(context.Background(), "user-a", "note-1")
	if err != nil {
		t.Fatalf("读取笔记失败: %v", err)
	}
	if note.State != entity.NoteStateNormal || note.Revision != 2 {
		t.Fatalf("笔记状态 = %q revision=%d", note.State, note.Revision)
	}
	if note.Content != "## 第 1 页\n\n示例内容" {
		t.Fatalf("正文 = %q", note.Content)
	}
	if !strings.HasPrefix(note.Title, "未命名笔记") {
		t.Fatalf("标题 = %q，期望「未命名笔记 + 时间」", note.Title)
	}
	if f.remaining(t, "user-a") != valueobject.DailyPageQuota-1 {
		t.Fatalf("成功后余额 = %d，期望 %d", f.remaining(t, "user-a"), valueobject.DailyPageQuota-1)
	}
}

/* TestRunJobUsesFirstHeading 校验取正文首个一级标题作为笔记标题 */
func TestRunJobUsesFirstHeading(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	f.vision.result = &port.VisionResult{Markdown: "# 矩阵的秩\n\n正文", Provider: "stub"}

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行任务失败: %v", err)
	}
	note, _ := f.notes.GetByID(context.Background(), "user-a", "note-1")
	if note.Title != "矩阵的秩" {
		t.Fatalf("标题 = %q，期望 矩阵的秩", note.Title)
	}
}

/* TestRunJobFailuresRefund 校验供应商失败、空结果与超时都会置失败并返还额度 */
func TestRunJobFailuresRefund(t *testing.T) {
	cases := []struct {
		name     string
		result   *port.VisionResult
		err      error
		wantCode entity.JobFailureCode
	}{
		{"供应商不可用", nil, port.ErrProviderUnavailable, entity.JobFailureProviderUnavailable},
		{"图片不可读", nil, port.ErrImageUnreadable, entity.JobFailureImageUnreadable},
		{"内容不支持", nil, port.ErrContentUnsupported, entity.JobFailureContentUnsupported},
		{"超时", nil, context.DeadlineExceeded, entity.JobFailureTimeout},
		{"空结果", &port.VisionResult{Markdown: "   "}, nil, entity.JobFailureEmptyResult},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			f.seedDraft(t, "note-1", "user-a", 2)
			f.seedJob(t, "job-1", "note-1", "user-a", 2, entity.JobStateQueued)
			f.vision.result = testCase.result
			f.vision.err = testCase.err

			if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
				t.Fatalf("执行任务失败: %v", err)
			}
			job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
			if job.State != entity.JobStateFailed || job.FailureCode != testCase.wantCode {
				t.Fatalf("任务 = %+v，期望 FAILED/%s", job, testCase.wantCode)
			}
			if job.FailureUserMessage == "" {
				t.Fatal("失败文案为空")
			}
			if f.remaining(t, "user-a") != valueobject.DailyPageQuota {
				t.Fatalf("失败后余额 = %d，期望全额返还 %d", f.remaining(t, "user-a"), valueobject.DailyPageQuota)
			}
			note, _ := f.notes.GetByID(context.Background(), "user-a", "note-1")
			if note.State != entity.NoteStateDraft || note.Content != "" {
				t.Fatalf("失败后笔记 = %+v，期望保持 DRAFT 且正文为空", note)
			}
		})
	}
}

/* TestCancelJobRefundsOnce 校验取消返还额度，且重复取消返回状态非法 */
func TestCancelJobRefundsOnce(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 3)
	f.seedJob(t, "job-1", "note-1", "user-a", 3, entity.JobStateQueued)

	view, err := f.service.CancelJob(context.Background(), "user-a", "note-1")
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if view.State != string(entity.JobStateCanceled) {
		t.Fatalf("状态 = %q，期望 CANCELED", view.State)
	}
	if f.remaining(t, "user-a") != valueobject.DailyPageQuota {
		t.Fatalf("取消后余额 = %d，期望 %d", f.remaining(t, "user-a"), valueobject.DailyPageQuota)
	}

	if _, err := f.service.CancelJob(context.Background(), "user-a", "note-1"); codeOf(err) != errs.CodeConversionStateInvalid {
		t.Fatalf("重复取消应返回 CONVERSION_STATE_INVALID，实际 %v", err)
	}
}

/* TestRetryJobCreatesNewTask 校验重试复用页面、尝试次数递增并重新扣额度 */
func TestRetryJobCreatesNewTask(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 2)
	f.seedJob(t, "job-1", "note-1", "user-a", 2, entity.JobStateFailed)

	view, err := f.service.RetryJob(context.Background(), "user-a", "note-1")
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if view.State != string(entity.JobStateQueued) || view.TotalPages != 2 || view.QuotaCharged != 2 {
		t.Fatalf("新任务 = %+v", view)
	}
	stored, _ := f.jobs.GetByID(context.Background(), "user-a", view.ID)
	if stored.Attempt != 2 {
		t.Fatalf("尝试次数 = %d，期望 2", stored.Attempt)
	}
	if f.nextQueued(t) != view.ID {
		t.Fatalf("队列中的任务 = %q，期望 %q", view.ID, view.ID)
	}
	if f.remaining(t, "user-a") != valueobject.DailyPageQuota-2 {
		t.Fatalf("重试后余额 = %d，期望 %d", f.remaining(t, "user-a"), valueobject.DailyPageQuota-2)
	}
}

/* TestRetryJobRejectsRunning 校验进行中的任务不可重试 */
func TestRetryJobRejectsRunning(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)

	if _, err := f.service.RetryJob(context.Background(), "user-a", "note-1"); codeOf(err) != errs.CodeConversionStateInvalid {
		t.Fatalf("进行中任务重试应返回 CONVERSION_STATE_INVALID，实际 %v", err)
	}
}

/* TestGetJobMissing 校验从未提交转换时返回 CONVERSION_NOT_FOUND */
func TestGetJobMissing(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 0)

	if _, err := f.service.GetJob(context.Background(), "user-a", "note-1"); codeOf(err) != errs.CodeConversionNotFound {
		t.Fatalf("无任务应返回 CONVERSION_NOT_FOUND，实际 %v", err)
	}
}

/* TestRunJobIgnoresCanceled 校验已取消任务不再执行且不重复返还 */
func TestRunJobIgnoresCanceled(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateCanceled)
	if err := f.ledger.Refund(context.Background(), "user-a", "job-1", 1); err != nil {
		t.Fatalf("返还额度: %v", err)
	}

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行已取消任务不应报错: %v", err)
	}
	job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if job.State != entity.JobStateCanceled {
		t.Fatalf("状态 = %q，期望保持 CANCELED", job.State)
	}
	if f.remaining(t, "user-a") != valueobject.DailyPageQuota {
		t.Fatalf("余额 = %d，期望 %d", f.remaining(t, "user-a"), valueobject.DailyPageQuota)
	}
}

/* TestCancelJobAfterSuccessKeepsSucceeded 校验成功终态不会被取消覆盖，也不重复返还 */
func TestCancelJobAfterSuccessKeepsSucceeded(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	f.vision.result = &port.VisionResult{Markdown: "# 已完成\n\n正文", Provider: "stub"}

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行任务失败: %v", err)
	}
	if _, err := f.service.CancelJob(context.Background(), "user-a", "note-1"); codeOf(err) != errs.CodeConversionStateInvalid {
		t.Fatalf("成功任务取消应返回 CONVERSION_STATE_INVALID，实际 %v", err)
	}
	job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if job.State != entity.JobStateSucceeded {
		t.Fatalf("状态 = %q，期望保持 SUCCEEDED", job.State)
	}
	if f.remaining(t, "user-a") != valueobject.DailyPageQuota-1 {
		t.Fatalf("余额 = %d，成功任务不应被返还", f.remaining(t, "user-a"))
	}
}

/* TestRetryAppendJobKeepsAppendedPageCount 校验追加任务重试只按新增页面重算，不重复扣额 */
func TestRetryAppendJobKeepsAppendedPageCount(t *testing.T) {
	f := newFixture(t)
	userID := "user-append"
	f.seedNamedNote(t, "note-append", userID, "笔记", "# 标题\n\n已有正文")
	/* 页面 1 属于原任务，页面 2、3 属于失败的追加任务 */
	f.appendPage(t, "note-append", userID, 1)
	f.appendPage(t, "note-append", userID, 2)
	f.appendPage(t, "note-append", userID, 3)

	now := f.clock.Now()
	failed := entity.NewConversionJob("job-append-1", "note-append", userID,
		entity.JobKindAppend, 2, 2, 1, now)
	failed.Fail(entity.JobFailureProviderUnavailable, now)
	if err := f.jobs.Create(context.Background(), failed); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	view, err := f.service.RetryJob(context.Background(), userID, "note-append")
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if view.TotalPages != 2 || view.QuotaCharged != 2 {
		t.Fatalf("重试任务 = %+v，期望只重算新增的 2 页", view)
	}
	if f.remaining(t, userID) != valueobject.DailyPageQuota-2 {
		t.Fatalf("余额 = %d，期望按 2 页扣减", f.remaining(t, userID))
	}
}

/* TestRecoverPendingRequeuesJobs 校验启动恢复把残留 RUNNING 回落并重新投递全部排队任务 */
func TestRecoverPendingRequeuesJobs(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	f.seedDraft(t, "note-2", "user-b", 1)
	f.seedJob(t, "job-2", "note-2", "user-b", 1, entity.JobStateQueued)
	f.seedDraft(t, "note-3", "user-c", 1)
	f.seedJob(t, "job-3", "note-3", "user-c", 1, entity.JobStateQueued)
	/* 模拟上次进程中断：任务停留在 RUNNING */
	stored, _ := f.jobs.GetByID(context.Background(), "user-c", "job-3")
	stored.Start(f.clock.Now())
	if _, err := f.jobs.Update(context.Background(), stored, entity.JobStateQueued); err != nil {
		t.Fatalf("置为处理中失败: %v", err)
	}

	count, err := f.service.RecoverPending(context.Background(), 100)
	if err != nil {
		t.Fatalf("启动恢复失败: %v", err)
	}
	if count != 3 {
		t.Fatalf("重新投递数量 = %d，期望 3", count)
	}
	for index := 0; index < 3; index++ {
		f.nextQueued(t)
	}
	released, _ := f.jobs.GetByID(context.Background(), "user-c", "job-3")
	if released.State != entity.JobStateQueued || released.StartedAt != nil {
		t.Fatalf("遗留任务 = %+v，期望回落为 QUEUED 且清空开始时间", released)
	}
}

/* TestRunJobRespectsContext 校验供应商取消映射为超时并返还 */
func TestRunJobRespectsContext(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	f.vision.err = errors.New("供应商连接中断")

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行任务失败: %v", err)
	}
	job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if job.State != entity.JobStateFailed || job.FinishedAt == nil {
		t.Fatalf("任务 = %+v", job)
	}
}

/* TestCanceledContextRequeuesInFlightJob 校验进程停机取消时不落 FAILED，而是回落排队且不退款 */
func TestCanceledContextRequeuesInFlightJob(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	charged := f.remaining(t, "user-a")
	stored, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	stored.Start(f.clock.Now())
	if _, err := f.jobs.Update(context.Background(), stored, entity.JobStateQueued); err != nil {
		t.Fatalf("置为处理中失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.service.handleFailure(ctx, "user-a", "job-1", entity.JobFailureInternal)

	job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if job.State != entity.JobStateQueued || job.StartedAt != nil || job.FailureCode != "" {
		t.Fatalf("取消后任务 = %+v，期望回落 QUEUED 且无失败码", job)
	}
	if f.remaining(t, "user-a") != charged {
		t.Fatalf("余额 = %d，期望取消不改动 %d", f.remaining(t, "user-a"), charged)
	}
}

/* TestSettlementConflictMarksNoteModified 校验结算冲突不写正文，按 NOTE_MODIFIED 收口 */
func TestSettlementConflictMarksNoteModified(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 1)
	f.seedJob(t, "job-1", "note-1", "user-a", 1, entity.JobStateQueued)
	stored, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	stored.Start(f.clock.Now())
	if _, err := f.jobs.Update(context.Background(), stored, entity.JobStateQueued); err != nil {
		t.Fatalf("置为处理中失败: %v", err)
	}

	f.service.handleSettlementError(context.Background(), "user-a", "job-1", errNoteModified)

	job, _ := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if job.State != entity.JobStateFailed || job.FailureCode != entity.JobFailureNoteModified {
		t.Fatalf("冲突后任务 = %+v，期望 FAILED/NOTE_MODIFIED", job)
	}
	if job.FailureUserMessage != "笔记在转换期间被修改，请重试" {
		t.Fatalf("失败提示 = %q", job.FailureUserMessage)
	}
}
