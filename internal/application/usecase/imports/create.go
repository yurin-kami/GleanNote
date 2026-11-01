/*
 * 批量导入：串行创建草稿并提交转换，逐项记录结果。
 *
 * 规则来源：PRD FR-17 —— 每张图片一篇笔记或按顺序合并一篇（后者单篇 ≤20 张），
 * 全部图片 ≤50 张；单项失败不影响其余项，整体状态按成功/失败数量落定。
 * 参考：docs/API.md#511-导出与导入（p1）
 */
package imports

import (
	"context"
	"fmt"
	"strings"

	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 批量导入上限：单次 50 张，合并模式单篇 20 张（PRD FR-17） */
const (
	MaxImportUploads = 50
	MaxMergeUploads  = 20
)

/*
 * CreateImport 发起批量导入。
 * 当前阶段串行处理并同步返回终态；单项状态表示「草稿已创建且转换任务已提交」。
 */
func (s *Service) CreateImport(ctx context.Context, input CreateImportInput) (ImportView, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return ImportView{}, errs.New(errs.CodeValidationFailed, "缺少用户标识")
	}
	if strings.TrimSpace(input.FolderID) == "" {
		return ImportView{}, errs.New(errs.CodeValidationFailed, "folderId 必填")
	}
	mode, err := parseMode(input.Mode)
	if err != nil {
		return ImportView{}, err
	}
	uploadIDs, err := validateUploadIDs(input.UploadIDs, mode)
	if err != nil {
		return ImportView{}, err
	}

	/* 目标目录必须先可用，避免为注定失败的批次留下任务记录 */
	folder, err := s.deps.Folders.GetByID(ctx, input.UserID, input.FolderID)
	if err != nil {
		return ImportView{}, err
	}
	if folder.IsDeleted() {
		return ImportView{}, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}

	importID, err := s.ids.New()
	if err != nil {
		return ImportView{}, errs.New(errs.CodeInternal, "生成导入标识失败").WithCause(err)
	}
	now := s.clock.Now()
	job := entity.NewImportJob(importID, input.UserID, input.FolderID, mode, uploadIDs, now)
	if err := s.deps.Jobs.Create(ctx, job); err != nil {
		return ImportView{}, err
	}
	job.MarkRunning()
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ImportView{}, err
	}

	if mode == entity.ImportModeMerge {
		s.processMerge(ctx, job, uploadIDs)
	} else {
		s.processPerImage(ctx, job, uploadIDs)
	}
	job.Finish(s.clock.Now())
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ImportView{}, err
	}
	return toView(job), nil
}

/* processPerImage 逐张创建草稿，单项失败不阻断后续项 */
func (s *Service) processPerImage(ctx context.Context, job *entity.ImportJob, uploadIDs []string) {
	for index, uploadID := range uploadIDs {
		result, err := s.deps.NoteCreator.CreateFromPages(ctx, noteusecase.CreateFromPagesInput{
			UserID:    job.UserID,
			FolderID:  job.FolderID,
			UploadIDs: []string{uploadID},
		})
		if err != nil {
			job.FailItem(index, failureCode(err))
			continue
		}
		job.SucceedItem(index, result.Note.ID)
	}
}

/* processMerge 把所有图片合并为一篇，成功或失败都落在每个导入项上 */
func (s *Service) processMerge(ctx context.Context, job *entity.ImportJob, uploadIDs []string) {
	result, err := s.deps.NoteCreator.CreateFromPages(ctx, noteusecase.CreateFromPagesInput{
		UserID:    job.UserID,
		FolderID:  job.FolderID,
		UploadIDs: uploadIDs,
	})
	if err != nil {
		code := failureCode(err)
		for index := range uploadIDs {
			job.FailItem(index, code)
		}
		return
	}
	for index := range uploadIDs {
		job.SucceedItem(index, result.Note.ID)
	}
}

/* GetImport 查询导入进度与逐项结果 */
func (s *Service) GetImport(ctx context.Context, userID, importID string) (ImportView, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(importID) == "" {
		return ImportView{}, errs.New(errs.CodeValidationFailed, "导入任务标识不能为空")
	}
	job, err := s.deps.Jobs.GetByID(ctx, userID, importID)
	if err != nil {
		return ImportView{}, err
	}
	return toView(job), nil
}

/* parseMode 校验导入模式，大小写不敏感 */
func parseMode(raw string) (entity.ImportMode, error) {
	switch entity.ImportMode(strings.ToUpper(strings.TrimSpace(raw))) {
	case entity.ImportModePerImage:
		return entity.ImportModePerImage, nil
	case entity.ImportModeMerge:
		return entity.ImportModeMerge, nil
	default:
		return "", errs.New(errs.CodeValidationFailed, "mode 必须为 PER_IMAGE 或 MERGE")
	}
}

/* validateUploadIDs 校验 1–50 张、无重复，并对 MERGE 额外限制为 ≤20 张 */
func validateUploadIDs(raw []string, mode entity.ImportMode) ([]string, error) {
	if len(raw) == 0 || len(raw) > MaxImportUploads {
		return nil, errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("uploadIds 必须在 1–%d 个之间", MaxImportUploads))
	}
	if mode == entity.ImportModeMerge && len(raw) > MaxMergeUploads {
		return nil, errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("合并导入单篇最多 %d 张，请改用 PER_IMAGE", MaxMergeUploads))
	}
	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, len(raw))
	for index, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("uploadIds[%d] 不能为空", index))
		}
		if _, duplicated := seen[id]; duplicated {
			return nil, errs.New(errs.CodeValidationFailed, "uploadIds 存在重复项").WithMeta("uploadId", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

/* toView 把导入任务映射为对外视图 */
func toView(job *entity.ImportJob) ImportView {
	items := make([]ImportItemView, 0, len(job.Items))
	for _, item := range job.Items {
		items = append(items, ImportItemView{
			UploadID:    item.UploadID,
			NoteID:      item.NoteID,
			State:       string(item.State),
			FailureCode: item.FailureCode,
		})
	}
	return ImportView{
		ID:        job.ID,
		State:     string(job.State),
		Total:     job.Total,
		Succeeded: job.Succeeded,
		Failed:    job.Failed,
		Items:     items,
	}
}

/* failureCode 把错误收敛为导入项失败码；非业务错误统一记为 INTERNAL */
func failureCode(err error) string {
	if code, ok := errs.CodeOf(err); ok {
		return string(code)
	}
	return string(errs.CodeInternal)
}
