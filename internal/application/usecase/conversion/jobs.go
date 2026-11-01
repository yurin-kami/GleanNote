/* 转换任务的查询、取消与重试用例 */
package conversion

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/*
 * GetJob 返回笔记最近一次转换任务。
 * 参考：docs/API.md#551-任务对象
 */
func (s *Service) GetJob(ctx context.Context, userID, noteID string) (JobView, error) {
	if err := requireUser(userID); err != nil {
		return JobView{}, err
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return JobView{}, err
	}
	job, err := s.deps.Jobs.GetLatestByNote(ctx, userID, noteID)
	if err != nil {
		return JobView{}, err
	}
	return toJobView(job), nil
}

/*
 * CancelJob 取消排队中或处理中的任务，并等额返还额度。
 * 规则：仅 QUEUED/RUNNING 可取消，页面图片保留；重复取消返回 CONVERSION_STATE_INVALID。
 * 参考：docs/API.md#552-post-v1notesnoteidconversionjobcancel
 */
func (s *Service) CancelJob(ctx context.Context, userID, noteID string) (JobView, error) {
	if err := requireUser(userID); err != nil {
		return JobView{}, err
	}
	now := s.clock.Now()
	var canceled *entity.ConversionJob
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := s.deps.Notes.GetByID(txCtx, userID, noteID); err != nil {
			return err
		}
		latest, err := s.deps.Jobs.GetLatestByNote(txCtx, userID, noteID)
		if err != nil {
			return err
		}
		/* 加锁读取：与 Worker 的成功/失败结算串行化，避免取消与成功互相覆盖 */
		job, err := s.deps.Jobs.GetForUpdate(txCtx, userID, latest.ID)
		if err != nil {
			return err
		}
		previous := job.State
		if err := job.Cancel(now); err != nil {
			return err
		}
		applied, err := s.deps.Jobs.Update(txCtx, job, previous)
		if err != nil {
			return err
		}
		if !applied {
			return errs.New(errs.CodeConversionStateInvalid, "任务状态已变化，请刷新后重试")
		}
		/* 返还与取消同事务：Refund 按 (jobID,"refund") 幂等，重复调用不会多退 */
		if err := s.deps.Quota.Refund(txCtx, userID, job.ID, job.QuotaCharged); err != nil {
			return err
		}
		canceled = job
		return nil
	})
	if err != nil {
		return JobView{}, err
	}
	return toJobView(canceled), nil
}

/*
 * RetryJob 创建新任务并复用已有页面，重新扣减额度。
 * 前置：笔记为 DRAFT/NORMAL 且有页面；最近任务已失败或取消。
 * 参考：docs/API.md#553-post-v1notesnoteidconversionjobretry
 */
func (s *Service) RetryJob(ctx context.Context, userID, noteID string) (JobView, error) {
	if err := requireUser(userID); err != nil {
		return JobView{}, err
	}
	note, err := s.deps.Notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return JobView{}, err
	}
	if note.State != entity.NoteStateDraft && note.State != entity.NoteStateNormal {
		return JobView{}, errs.New(errs.CodeNoteStateInvalid, "当前笔记状态不支持重试")
	}
	pages, err := s.deps.Pages.ListAll(ctx, userID, noteID)
	if err != nil {
		return JobView{}, err
	}
	if len(pages) == 0 {
		return JobView{}, errs.New(errs.CodeConversionStateInvalid, "笔记没有可重试的页面")
	}
	if len(pages) > valueobject.MaxNotePages {
		return JobView{}, errs.New(errs.CodeNotePageLimit, "单篇笔记最多 50 张图片")
	}

	latest, err := s.deps.Jobs.GetLatestByNote(ctx, userID, noteID)
	if err != nil {
		return JobView{}, err
	}
	if latest.State != entity.JobStateFailed && latest.State != entity.JobStateCanceled {
		return JobView{}, errs.New(errs.CodeConversionStateInvalid, "当前转换状态不支持重试")
	}

	/*
	 * 重试只重算原任务覆盖的页面：追加任务保持原 TotalPages，
	 * 否则会把历史页面重新识别并向正文重复追加，额度也会按历史总量多扣。
	 */
	retryPages := len(pages)
	if latest.Kind == entity.JobKindAppend {
		retryPages = latest.TotalPages
	}
	if retryPages <= 0 || retryPages > len(pages) {
		return JobView{}, errs.New(errs.CodeConversionStateInvalid, "追加的页面已不存在，无法重试")
	}

	used, err := s.deps.Storage.Used(ctx, userID)
	if err != nil {
		return JobView{}, err
	}
	if used > valueobject.StorageLimitBytes {
		return JobView{}, errs.New(errs.CodeStorageExceeded, "云端空间不足")
	}

	now := s.clock.Now()
	jobID, err := s.newID()
	if err != nil {
		return JobView{}, err
	}
	job := entity.NewConversionJob(jobID, noteID, userID, latest.Kind,
		retryPages, retryPages, latest.Attempt+1, now)
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.ensureNoActiveJob(txCtx, userID, noteID); err != nil {
			return err
		}
		if err := s.deps.Quota.Charge(txCtx, userID, jobID, bizdate.Day(now),
			retryPages, valueobject.DailyPageQuota); err != nil {
			return err
		}
		return s.deps.Jobs.Create(txCtx, job)
	})
	if err != nil {
		return JobView{}, err
	}

	if err := s.deps.Queue.Enqueue(ctx, userID, jobID); err != nil {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureProviderUnavailable)
		return JobView{}, errs.New(errs.CodeInternal, "提交转换任务失败").WithCause(err)
	}
	return toJobView(job), nil
}

/*
 * ensureNoActiveJob 拒绝同一笔记重复提交转换任务。
 * 重复提交会让两批页面争用同一段正文，并造成额度重复排队。
 */
func (s *Service) ensureNoActiveJob(ctx context.Context, userID, noteID string) error {
	active, err := s.deps.Jobs.ListActiveByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, job := range active {
		if job.NoteID == noteID {
			return errs.New(errs.CodeConversionStateInvalid, "该笔记已有进行中的转换任务")
		}
	}
	return nil
}

/*
 * RecoverPending 在 Worker 启动时恢复上次进程中断遗留的任务。
 * 先把残留的 RUNNING 任务回落为排队，再把全部排队任务重新投递；
 * 重复投递由状态机 CAS 保证只有一次真正执行。
 * 约束：回落是全表批量，仅单实例部署可在启动时调用；扩容前需按实例租约收敛范围。
 */
func (s *Service) RecoverPending(ctx context.Context, limit int) (int, error) {
	if _, err := s.deps.Jobs.ReleaseRunning(ctx, s.clock.Now()); err != nil {
		return 0, err
	}
	return s.requeue(ctx, s.clock.Now(), limit)
}

/* RequeueStale 补偿入队长时间停留在排队状态的任务，避免投递丢失后永久卡住 */
func (s *Service) RequeueStale(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	return s.requeue(ctx, s.clock.Now().Add(-olderThan), limit)
}

/* requeue 把更新时间早于 before 的排队任务重新投递到队列 */
func (s *Service) requeue(ctx context.Context, before time.Time, limit int) (int, error) {
	pending, err := s.deps.Jobs.ListPending(ctx, before, limit)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, job := range pending {
		if err := s.deps.Queue.Enqueue(ctx, job.UserID, job.ID); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

/* toJobView 把任务实体映射为对外视图 */
func toJobView(job *entity.ConversionJob) JobView {
	return JobView{
		ID:                 job.ID,
		NoteID:             job.NoteID,
		Kind:               string(job.Kind),
		State:              string(job.State),
		Phase:              string(job.Phase),
		TotalPages:         job.TotalPages,
		ProcessedPages:     job.ProcessedPages,
		FailureCode:        string(job.FailureCode),
		FailureUserMessage: job.FailureUserMessage,
		QuotaCharged:       job.QuotaCharged,
		CreatedAt:          job.CreatedAt,
		StartedAt:          job.StartedAt,
		FinishedAt:         job.FinishedAt,
	}
}
