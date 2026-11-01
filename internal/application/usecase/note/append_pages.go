/* 向已有笔记追加页面并提交追加转换的用例 */
package note

import (
	"context"
	"fmt"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/*
 * AppendPages 向状态为 NORMAL 的笔记追加页面并提交一个 APPEND 转换任务。
 * 约束：单次 1–20 张、累计不超过 50 张；校验顺序与创建一致：状态 → 页面上限 → 额度 → 容量 → 上传有效性 → 单事务写入。
 * 追加任务只处理新增页面，转换结果由 Worker 追加到当时最新正文末尾（PRD FR-18）。
 * 参考：docs/API.md#5310-post-v1notesnoteidappendpagesp1、docs/ARCHITECTURE.md#88-额度与容量
 */
func (s *Service) AppendPages(ctx context.Context, input AppendPagesInput) (AppendPagesResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return AppendPagesResult{}, err
	}
	if strings.TrimSpace(input.NoteID) == "" {
		return AppendPagesResult{}, errs.New(errs.CodeValidationFailed, "noteId 必填")
	}
	if err := validateUploadIDs(input.UploadIDs); err != nil {
		return AppendPagesResult{}, err
	}

	/* 读取笔记并校验状态：只有 NORMAL 才允许追加，回收站中的笔记必须先恢复 */
	now := s.clock.Now()
	quotaDate := bizdate.Day(now)
	pageCount := len(input.UploadIDs)

	note, err := s.deps.Notes.GetByID(ctx, input.UserID, input.NoteID)
	if err != nil {
		return AppendPagesResult{}, err
	}
	if note.State != entity.NoteStateNormal {
		return AppendPagesResult{}, errs.New(errs.CodeNoteStateInvalid, "仅正常状态的笔记可追加页面")
	}

	/* 累计页面上限：已有页面数 + 本次张数不得超过 50 */
	existing, err := s.deps.Pages.CountByNote(ctx, input.UserID, input.NoteID)
	if err != nil {
		return AppendPagesResult{}, err
	}
	if existing+pageCount > valueobject.MaxNotePages {
		return AppendPagesResult{}, errs.New(errs.CodeNotePageLimit,
			fmt.Sprintf("单篇笔记最多 %d 张图片，当前已有 %d 张", valueobject.MaxNotePages, existing)).
			WithMeta("pageCount", existing).
			WithMeta("requested", pageCount)
	}

	/* ① 额度 */
	remaining, err := s.deps.Quota.Remaining(ctx, input.UserID, quotaDate)
	if err != nil {
		return AppendPagesResult{}, err
	}
	if remaining < pageCount {
		return AppendPagesResult{}, errs.New(errs.CodeQuotaExceeded,
			fmt.Sprintf("今日剩余额度 %d 张，本次需要 %d 张", remaining, pageCount)).
			WithMeta("remaining", remaining).
			WithMeta("requested", pageCount)
	}

	/* 读取上传实体用于汇总字节数，容量与额度都按本次实际提交张数计费 */
	uploads := make([]*entity.Upload, 0, pageCount)
	var totalBytes int64
	for _, uploadID := range input.UploadIDs {
		upload, err := s.deps.Uploads.GetByID(ctx, input.UserID, uploadID)
		if err != nil {
			return AppendPagesResult{}, err
		}
		uploads = append(uploads, upload)
		totalBytes += upload.Bytes
	}

	/* ② 容量 */
	used, err := s.deps.Storage.Used(ctx, input.UserID)
	if err != nil {
		return AppendPagesResult{}, err
	}
	if used+totalBytes > valueobject.StorageLimitBytes {
		return AppendPagesResult{}, errs.New(errs.CodeStorageExceeded, "云端空间不足，请清理后重试").
			WithMeta("usedBytes", used).
			WithMeta("requiredBytes", totalBytes)
	}

	/* ③ 上传有效性：先拦截已消费与已过期的会话，二者都不可再用于建页 */
	for index, upload := range uploads {
		if upload.Consumed() {
			return AppendPagesResult{}, errs.New(errs.CodeUploadNotReady,
				fmt.Sprintf("uploadIds[%d] 已用于其他笔记", index)).
				WithMeta("uploadId", upload.ID)
		}
		if upload.Expired(now) {
			upload.MarkExpired()
			if err := s.deps.Uploads.Update(ctx, upload); err != nil {
				return AppendPagesResult{}, err
			}
			return AppendPagesResult{}, errs.New(errs.CodeUploadExpired,
				fmt.Sprintf("uploadIds[%d] 会话已超时", index)).
				WithMeta("uploadId", upload.ID)
		}
		/* 未完成校验的会话不可建页，避免写入半成品页面 */
		if !upload.IsReady() {
			return AppendPagesResult{}, errs.New(errs.CodeUploadNotReady,
				fmt.Sprintf("uploadIds[%d] 尚未完成校验", index)).
				WithMeta("uploadId", upload.ID)
		}
	}

	/* 先分配页面与任务 ID，事务内只做写入，避免在事务里调用外部生成器 */
	pageIDs := make([]string, 0, pageCount)
	for range uploads {
		pageID, err := s.newID("页面")
		if err != nil {
			return AppendPagesResult{}, err
		}
		pageIDs = append(pageIDs, pageID)
	}
	jobID, err := s.newID("转换任务")
	if err != nil {
		return AppendPagesResult{}, err
	}

	var job *entity.ConversionJob
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		/* 事务内重读，避免并发删除或状态迁移后仍写入页面 */
		current, err := s.deps.Notes.GetByID(txCtx, input.UserID, input.NoteID)
		if err != nil {
			return err
		}
		if current.State != entity.NoteStateNormal {
			return errs.New(errs.CodeNoteStateInvalid, "仅正常状态的笔记可追加页面")
		}
		if err := s.ensureNoActiveJob(txCtx, input.UserID, input.NoteID); err != nil {
			return err
		}
		/* 事务内原子消费：并发使用同一 uploadId 时只有一个请求能成功追加 */
		for index, upload := range uploads {
			consumed, err := s.deps.Uploads.Consume(txCtx, input.UserID, upload.ID)
			if err != nil {
				return err
			}
			if !consumed {
				return errs.New(errs.CodeUploadNotReady,
					fmt.Sprintf("uploadIds[%d] 已被使用", index)).
					WithMeta("uploadId", upload.ID)
			}
		}
		/* 页面序号接在已有页面之后，保证追加页始终排在正文对应位置 */
		pages := make([]*entity.NotePage, 0, pageCount)
		for index, upload := range uploads {
			pages = append(pages, entity.NewNotePage(pageIDs[index], input.NoteID,
				input.UserID, existing+index+1, upload, now))
		}
		if err := s.deps.Pages.CreateBatch(txCtx, pages); err != nil {
			return err
		}
		/* 任务、额度与容量在同一事务落库：任一失败都整体回滚，避免只扣费未建页 */
		created := entity.NewConversionJob(jobID, input.NoteID, input.UserID,
			entity.JobKindAppend, pageCount, pageCount, 1, now)
		if err := s.deps.Jobs.Create(txCtx, created); err != nil {
			return err
		}
		if err := s.deps.Quota.Charge(txCtx, input.UserID, jobID, quotaDate,
			pageCount, valueobject.DailyPageQuota); err != nil {
			return err
		}
		if err := s.deps.Storage.Reserve(txCtx, input.UserID, totalBytes, valueobject.StorageLimitBytes); err != nil {
			return err
		}
		job = created
		return nil
	})
	/* 事务失败直接返回；事务成功但入队失败时补偿任务，避免额度被扣却无人消费 */
	if err != nil {
		return AppendPagesResult{}, err
	}

	if err := s.deps.Queue.Enqueue(ctx, input.UserID, jobID); err != nil {
		s.failSubmittedJob(context.WithoutCancel(ctx), input.UserID, jobID)
		return AppendPagesResult{}, errs.New(errs.CodeInternal, "提交转换任务失败").WithCause(err)
	}

	/* 返回入队后的最新余额，客户端无需再单独查询 */
	left, err := s.deps.Quota.Remaining(ctx, input.UserID, quotaDate)
	if err != nil {
		return AppendPagesResult{}, err
	}
	return AppendPagesResult{
		Job:   toSubmittedJob(job),
		Quota: QuotaView{Remaining: left, Charged: pageCount},
	}, nil
}
