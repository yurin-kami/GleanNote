/* 由已上传图片创建草稿笔记并提交转换的用例 */
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
 * CreateFromPages 由已确认的上传图片创建 DRAFT 笔记、页面与转换任务。
 * 校验顺序固定为：额度 → 存储容量 → 上传有效性 → 单事务写入（笔记+页面+任务+账本+容量）。
 * 参考：docs/API.md#534-post-v1notescreatefrompages、docs/ARCHITECTURE.md#88-额度与容量
 */
func (s *Service) CreateFromPages(ctx context.Context, input CreateFromPagesInput) (CreateFromPagesResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return CreateFromPagesResult{}, err
	}
	if strings.TrimSpace(input.FolderID) == "" {
		return CreateFromPagesResult{}, errs.New(errs.CodeValidationFailed, "folderId 必填")
	}
	if err := validateUploadIDs(input.UploadIDs); err != nil {
		return CreateFromPagesResult{}, err
	}
	title, err := valueobject.ParseNoteTitle(input.Title)
	if err != nil {
		return CreateFromPagesResult{}, err
	}

	now := s.clock.Now()
	quotaDate := bizdate.Day(now)
	pageCount := len(input.UploadIDs)

	/* ① 额度：先按图片张数判断，避免为注定失败的请求读取对象与目录 */
	remaining, err := s.deps.Quota.Remaining(ctx, input.UserID, quotaDate)
	if err != nil {
		return CreateFromPagesResult{}, err
	}
	if remaining < pageCount {
		return CreateFromPagesResult{}, errs.New(errs.CodeQuotaExceeded,
			fmt.Sprintf("今日剩余额度 %d 张，本次需要 %d 张", remaining, pageCount)).
			WithMeta("remaining", remaining).
			WithMeta("requested", pageCount)
	}

	uploads := make([]*entity.Upload, 0, pageCount)
	var totalBytes int64
	for _, uploadID := range input.UploadIDs {
		upload, err := s.deps.Uploads.GetByID(ctx, input.UserID, uploadID)
		if err != nil {
			return CreateFromPagesResult{}, err
		}
		uploads = append(uploads, upload)
		totalBytes += upload.Bytes
	}

	/* ② 容量：按原图字节累计，回收站内容同样占用（架构 8.8） */
	used, err := s.deps.Storage.Used(ctx, input.UserID)
	if err != nil {
		return CreateFromPagesResult{}, err
	}
	if used+totalBytes > valueobject.StorageLimitBytes {
		return CreateFromPagesResult{}, errs.New(errs.CodeStorageExceeded, "云端空间不足，请清理后重试").
			WithMeta("usedBytes", used).
			WithMeta("requiredBytes", totalBytes)
	}

	/* ③ 上传有效性：必须已通过 complete 校验、未被消费且未过期 */
	for index, upload := range uploads {
		if upload.Consumed() {
			return CreateFromPagesResult{}, errs.New(errs.CodeUploadNotReady,
				fmt.Sprintf("uploadIds[%d] 已用于其他笔记", index)).
				WithMeta("uploadId", upload.ID)
		}
		if upload.Expired(now) {
			upload.MarkExpired()
			if err := s.deps.Uploads.Update(ctx, upload); err != nil {
				return CreateFromPagesResult{}, err
			}
			return CreateFromPagesResult{}, errs.New(errs.CodeUploadExpired,
				fmt.Sprintf("uploadIds[%d] 会话已超时", index)).
				WithMeta("uploadId", upload.ID)
		}
		if !upload.IsReady() {
			return CreateFromPagesResult{}, errs.New(errs.CodeUploadNotReady,
				fmt.Sprintf("uploadIds[%d] 尚未完成校验", index)).
				WithMeta("uploadId", upload.ID)
		}
	}

	/* ④ 单事务写入：笔记 + 页面 + 任务 + 账本 + 容量 */
	noteID, err := s.newID("笔记")
	if err != nil {
		return CreateFromPagesResult{}, err
	}
	pageIDs := make([]string, 0, pageCount)
	for range uploads {
		pageID, err := s.newID("页面")
		if err != nil {
			return CreateFromPagesResult{}, err
		}
		pageIDs = append(pageIDs, pageID)
	}
	jobID, err := s.newID("转换任务")
	if err != nil {
		return CreateFromPagesResult{}, err
	}

	var created *entity.Note
	var job *entity.ConversionJob
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := s.activeFolder(txCtx, input.UserID, input.FolderID); err != nil {
			return err
		}
		/* 事务内原子消费：并发使用同一 uploadId 时只有一个请求能成功建页 */
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
		note := entity.NewDraftNote(noteID, input.UserID, input.FolderID, title, now)
		if err := s.deps.Notes.Create(txCtx, note); err != nil {
			return err
		}
		pages := make([]*entity.NotePage, 0, pageCount)
		for index, upload := range uploads {
			pages = append(pages, entity.NewNotePage(pageIDs[index], noteID, input.UserID, index+1, upload, now))
		}
		if err := s.deps.Pages.CreateBatch(txCtx, pages); err != nil {
			return err
		}
		createdJob := entity.NewConversionJob(jobID, noteID, input.UserID, entity.JobKindCreate,
			pageCount, pageCount, 1, now)
		if err := s.deps.Jobs.Create(txCtx, createdJob); err != nil {
			return err
		}
		if err := s.deps.Quota.Charge(txCtx, input.UserID, jobID, quotaDate,
			pageCount, valueobject.DailyPageQuota); err != nil {
			return err
		}
		if err := s.deps.Storage.Reserve(txCtx, input.UserID, totalBytes, valueobject.StorageLimitBytes); err != nil {
			return err
		}
		created = note
		job = createdJob
		return nil
	})
	if err != nil {
		return CreateFromPagesResult{}, err
	}

	/* 入队失败：补偿为 FAILED 并返还额度；笔记与页面保留，用户可重试转换 */
	if err := s.deps.Queue.Enqueue(ctx, input.UserID, jobID); err != nil {
		s.failSubmittedJob(context.WithoutCancel(ctx), input.UserID, jobID)
		return CreateFromPagesResult{}, errs.New(errs.CodeInternal, "提交转换任务失败").WithCause(err)
	}

	view, err := s.buildView(ctx, input.UserID, created)
	if err != nil {
		return CreateFromPagesResult{}, err
	}
	left, err := s.deps.Quota.Remaining(ctx, input.UserID, quotaDate)
	if err != nil {
		return CreateFromPagesResult{}, err
	}
	return CreateFromPagesResult{
		Note:  view,
		Job:   toSubmittedJob(job),
		Quota: QuotaView{Remaining: left, Charged: pageCount},
	}, nil
}

/* validateUploadIDs 校验 1–20 个非空且不重复；重复会让同一对象被计为两页 */
func validateUploadIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > valueobject.MaxUploadBatch {
		return errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("uploadIds 必须在 1–%d 个之间", valueobject.MaxUploadBatch))
	}
	seen := make(map[string]struct{}, len(ids))
	for index, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return errs.New(errs.CodeValidationFailed, fmt.Sprintf("uploadIds[%d] 不能为空", index))
		}
		if _, duplicated := seen[id]; duplicated {
			return errs.New(errs.CodeValidationFailed, "uploadIds 存在重复项").
				WithMeta("uploadId", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

/* failSubmittedJob 在入队失败后把任务置为异常并返还额度，返回 nil 即可安全重复调用 */
func (s *Service) failSubmittedJob(ctx context.Context, userID, jobID string) {
	_ = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Jobs.GetForUpdate(txCtx, userID, jobID)
		if err != nil || current.IsTerminal() {
			return nil
		}
		previous := current.State
		current.Fail(entity.JobFailureProviderUnavailable, s.clock.Now())
		applied, err := s.deps.Jobs.Update(txCtx, current, previous)
		if err != nil || !applied {
			return err
		}
		return s.deps.Quota.Refund(txCtx, current.UserID, current.ID, current.QuotaCharged)
	})
}

/* toSubmittedJob 把任务实体映射为提交响应中的摘要 */
func toSubmittedJob(job *entity.ConversionJob) ConversionJobView {
	return ConversionJobView{
		ID:             job.ID,
		State:          string(job.State),
		Phase:          string(job.Phase),
		TotalPages:     job.TotalPages,
		ProcessedPages: job.ProcessedPages,
	}
}
