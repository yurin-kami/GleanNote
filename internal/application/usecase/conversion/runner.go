/* 转换任务执行用例：Worker 领取任务后完成识图、写正文与额度结算 */
package conversion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/* errNoteModified 表示结算时发现笔记已被用户修改，用于映射 NOTE_MODIFIED 失败码 */
var errNoteModified = errors.New("笔记在转换期间被修改")

/*
 * RunJob 执行一次转换任务，由进程内 Worker 调用。
 * 顺序：置为处理中 → 读取页面并签名 → 调用供应商 → 写正文与标题 → 结算。
 * 失败会置为 FAILED 并等额返还额度；进程停机（上下文取消）时只回退排队，交给恢复逻辑重投。
 * 参考：docs/ARCHITECTURE.md#62-图片上传到转换完成、docs/API.md#63-上传与转换
 */
func (s *Service) RunJob(ctx context.Context, userID, jobID string) error {
	job, err := s.deps.Jobs.GetByID(ctx, userID, jobID)
	if err != nil {
		return err
	}
	if job.IsTerminal() {
		return nil
	}

	now := s.clock.Now()
	started := false
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil {
			return err
		}
		/* 仅排队中的任务可被领取：重复投递或已被取消时直接退出 */
		if current.State != entity.JobStateQueued {
			return nil
		}
		current.Start(now)
		applied, err := s.deps.Jobs.Update(txCtx, current, entity.JobStateQueued)
		if err != nil {
			return err
		}
		if !applied {
			return nil
		}
		job = current
		started = true
		return nil
	})
	if err != nil {
		return err
	}
	if !started {
		return nil
	}

	pages, err := s.deps.Pages.ListAll(ctx, job.UserID, job.NoteID)
	if err != nil {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureInternal)
		return nil
	}
	/* 追加任务只处理新增页面：它们按 Index 排在末尾，序号在原有页面之后 */
	pages = jobPages(job, pages)
	if len(pages) == 0 {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureEmptyResult)
		return nil
	}

	visionPages := make([]port.VisionPage, 0, len(pages))
	for _, page := range pages {
		url, _, err := s.deps.Objects.PresignGet(ctx, page.ObjectKey, visionURLTTL)
		if err != nil {
			s.handleFailure(ctx, userID, jobID, entity.JobFailureProviderUnavailable)
			return nil
		}
		visionPages = append(visionPages, port.VisionPage{Index: page.Index, URL: url})
	}

	runCtx, cancel := context.WithTimeout(ctx, ConversionTimeout)
	defer cancel()
	result, err := s.deps.Vision.Convert(runCtx, visionPages)
	if err != nil {
		s.handleFailure(ctx, userID, jobID, failureCodeFor(err))
		return nil
	}
	if result == nil || strings.TrimSpace(result.Markdown) == "" {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureEmptyResult)
		return nil
	}
	/*
	 * 识别已结束，先把阶段推进到 GENERATING 并落库，进度页无需等到终态才更新。
	 * 进度只是展示信息：落库失败不影响已完成的识图结果，后续结算事务会暴露真正的数据库故障。
	 */
	_ = s.advanceProgress(ctx, userID, jobID, job.TotalPages, entity.JobPhaseGenerating)

	finishedAt := s.clock.Now()
	title, err := deriveTitle(result.Markdown, finishedAt)
	if err != nil {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureContentUnsupported)
		return nil
	}
	content, err := valueobject.ParseNoteContent(result.Markdown)
	if err != nil {
		s.handleFailure(ctx, userID, jobID, entity.JobFailureContentUnsupported)
		return nil
	}

	settled := false
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil {
			return err
		}
		/* 用户在转换期间取消或任务已被并发置为终态：不写正文，也不重复返还 */
		if current.State != entity.JobStateRunning {
			return nil
		}
		note, err := s.deps.Notes.GetByID(txCtx, job.UserID, job.NoteID)
		if err != nil {
			return err
		}
		/* 追加任务：结果拼接到当时最新正文末尾，标题保持不变（PRD FR-18） */
		if job.Kind == entity.JobKindAppend {
			merged, parseErr := valueobject.ParseNoteContent(mergeAppendContent(note.Content, result.Markdown))
			if parseErr != nil {
				return parseErr
			}
			content = merged
			title = valueobject.NoteTitle(note.Title)
		}
		/*
		 * 以读取时的 revision 作为乐观锁基线：用户在此期间的自动保存会抢先提交，
		 * 条件更新失败而不是把用户编辑覆盖回旧 revision。
		 */
		baseRevision := note.Revision
		if err := note.Save(title, content, finishedAt); err != nil {
			return err
		}
		applied, err := s.deps.Notes.UpdateWithRevision(txCtx, note, baseRevision)
		if err != nil {
			return err
		}
		if !applied {
			return errNoteModified
		}
		/* 转换成功同样落一条 CONVERSION 版本（PRD FR-16） */
		if err := s.deps.Versions.Record(txCtx, job.UserID, note.ID, "CONVERSION", note.Content); err != nil {
			return err
		}
		current.Advance(current.TotalPages, entity.JobPhaseGenerating, finishedAt)
		current.Succeed(finishedAt)
		applied, err = s.deps.Jobs.Update(txCtx, current, entity.JobStateRunning)
		if err != nil {
			return err
		}
		if !applied {
			/* 状态在结算期间被改动：回滚正文写入，交给取消方保留终态 */
			return errs.New(errs.CodeConversionStateInvalid, "任务状态已变化")
		}
		settled = true
		return nil
	})
	if err != nil {
		s.handleSettlementError(ctx, userID, jobID, err)
		return nil
	}
	if !settled {
		return nil
	}
	return nil
}

/*
 * handleSettlementError 把结算事务错误映射为任务失败码。
 * 笔记被用户修改时放弃写入；取消或并发终态已生效时保留终态；其余视为基础设施故障。
 */
func (s *Service) handleSettlementError(ctx context.Context, userID, jobID string, err error) {
	if errs.IsCode(err, errs.CodeConversionStateInvalid) {
		return
	}
	code := entity.JobFailureInternal
	if errors.Is(err, errNoteModified) {
		code = entity.JobFailureNoteModified
	}
	s.handleFailure(ctx, userID, jobID, code)
}

/*
 * handleFailure 记录任务失败。
 * 上下文已取消（进程停机）时把任务回退为排队，由启动恢复或补偿协程重新投递，避免滚动发布丢任务。
 */
func (s *Service) handleFailure(ctx context.Context, userID, jobID string, code entity.JobFailureCode) {
	if ctx.Err() != nil {
		s.requeueJob(context.WithoutCancel(ctx), userID, jobID)
		return
	}
	s.failJob(context.WithoutCancel(ctx), userID, jobID, code)
}

/* requeueJob 把处理中的任务回落为排队且不退款；已终态或已排队的任务保持不变 */
func (s *Service) requeueJob(ctx context.Context, userID, jobID string) {
	now := s.clock.Now()
	_ = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil || current.State != entity.JobStateRunning {
			return nil
		}
		previous := current.State
		current.Requeue(now)
		_, err = s.deps.Jobs.Update(txCtx, current, previous)
		return err
	})
}

/*
 * advanceProgress 在独立短事务里推进任务阶段，与结算事务分离，避免长时间持锁。
 * 仅处理中的任务可推进；取消或已完成时直接跳过。
 */
func (s *Service) advanceProgress(ctx context.Context, userID, jobID string, processed int, phase entity.JobPhase) error {
	now := s.clock.Now()
	return s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil {
			return err
		}
		if current.State != entity.JobStateRunning {
			return nil
		}
		current.Advance(processed, phase, now)
		_, err = s.deps.Jobs.Update(txCtx, current, entity.JobStateRunning)
		return err
	})
}

/*
 * failJob 把任务置为失败并返还额度。
 * 幂等：任务已是终态时直接返回；返还按 (jobID,"refund") 去重，取消与失败不会重复退款。
 */
func (s *Service) failJob(ctx context.Context, userID, jobID string, code entity.JobFailureCode) {
	now := s.clock.Now()
	_ = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil {
			return nil
		}
		if current.IsTerminal() {
			return nil
		}
		previous := current.State
		current.Fail(code, now)
		applied, err := s.deps.Jobs.Update(txCtx, current, previous)
		if err != nil {
			return err
		}
		if !applied {
			/* 并发取消或成功已生效：不再覆盖终态，也不重复返还 */
			return nil
		}
		return s.deps.Quota.Refund(txCtx, current.UserID, current.ID, current.QuotaCharged)
	})
}

/* failureCodeFor 把供应商归一错误映射为任务失败码，未知错误按供应商不可用处理 */
func failureCodeFor(err error) entity.JobFailureCode {
	switch {
	case errors.Is(err, port.ErrImageUnreadable):
		return entity.JobFailureImageUnreadable
	case errors.Is(err, port.ErrContentUnsupported):
		return entity.JobFailureContentUnsupported
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return entity.JobFailureTimeout
	case errors.Is(err, port.ErrProviderUnavailable):
		return entity.JobFailureProviderUnavailable
	default:
		return entity.JobFailureProviderUnavailable
	}
}

/*
 * jobPages 截取本次任务对应的页面。
 * 新建任务处理全部页面；追加任务只处理本次新增的 TotalPages 张页面，它们按 Index 排在末尾。
 * 追加任务重试时 TotalPages 保持不变，因此不会把历史页面重复识别与重复扣额。
 */
func jobPages(job *entity.ConversionJob, pages []*entity.NotePage) []*entity.NotePage {
	if job.Kind != entity.JobKindAppend {
		return pages
	}
	count := job.TotalPages
	if count <= 0 {
		return pages[:0]
	}
	if count > len(pages) {
		count = len(pages)
	}
	return pages[len(pages)-count:]
}

/* mergeAppendContent 把追加结果拼接到已有正文末尾，不覆盖、不重排原内容 */
func mergeAppendContent(existing, appended string) string {
	if strings.TrimSpace(existing) == "" {
		return appended
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + appended
}

/*
 * deriveTitle 取正文首个一级标题；没有一级标题时回落到「未命名笔记 + 北京时间」。
 * 约束：标题最多 100 个字符，超长时截断，避免供应商输出导致写入失败。
 */
func deriveTitle(markdown string, now time.Time) (valueobject.NoteTitle, error) {
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "# ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		if title == "" {
			continue
		}
		if runes := []rune(title); len(runes) > valueobject.MaxNoteTitleLength {
			title = string(runes[:valueobject.MaxNoteTitleLength])
		}
		return valueobject.ParseNoteTitle(title)
	}
	fallback := fmt.Sprintf("未命名笔记 %s", now.In(bizdate.Location).Format("01-02 15:04"))
	return valueobject.ParseNoteTitle(fallback)
}
