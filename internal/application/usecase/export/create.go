/*
 * 导出任务创建：校验目标 → 落任务 → 生成归档 → 写入对象存储 → 签发下载地址。
 *
 * 当前阶段为同步生成：POST 返回 202 时任务已进入终态；
 * 后续接入 Worker 时只需替换 generate 的调用位置，状态机与契约保持不变。
 * 参考：docs/API.md#511-导出与导入（p1）、PRD FR-14
 */
package export

import (
	"context"
	"fmt"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* CreateExport 发起导出并返回任务视图；生成失败时任务为 FAILED 并带上失败码 */
func (s *Service) CreateExport(ctx context.Context, input CreateExportInput) (ExportView, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return ExportView{}, errs.New(errs.CodeValidationFailed, "缺少用户标识")
	}
	format, err := parseFormat(input.Format)
	if err != nil {
		return ExportView{}, err
	}
	noteID := strings.TrimSpace(input.NoteID)
	folderID := strings.TrimSpace(input.FolderID)
	if (noteID == "") == (folderID == "") {
		return ExportView{}, errs.New(errs.CodeValidationFailed, "noteId 与 folderId 必须且只能提供一个")
	}
	if folderID != "" && format != entity.ExportFormatMarkdownZip {
		return ExportView{}, errs.New(errs.CodeValidationFailed, "目录导出仅支持 MARKDOWN_ZIP")
	}

	/* 目标校验放在建任务之前，让 400/404/409 不留下无意义任务记录 */
	if folderID != "" {
		if _, err := s.loadActiveFolder(ctx, input.UserID, folderID); err != nil {
			return ExportView{}, err
		}
	} else if _, err := s.loadExportableNote(ctx, input.UserID, noteID); err != nil {
		return ExportView{}, err
	}

	/* 归档生成不消耗额度、不计容量、不写版本（PRD FR-14） */
	exportID, err := s.ids.New()
	if err != nil {
		return ExportView{}, errs.New(errs.CodeInternal, "生成导出标识失败").WithCause(err)
	}
	now := s.clock.Now()
	job := entity.NewExportJob(exportID, input.UserID, noteID, folderID, format, now)
	if err := s.deps.Jobs.Create(ctx, job); err != nil {
		return ExportView{}, err
	}
	job.MarkRunning()
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ExportView{}, err
	}

	data, contentType, err := s.generate(ctx, input.UserID, noteID, folderID, format)
	if err != nil {
		return s.failJob(ctx, job, err)
	}

	objectKey := fmt.Sprintf("exports/%s/%s.%s", input.UserID, exportID, formatExtension(format))
	if err := s.deps.Objects.Put(ctx, objectKey, contentType, data); err != nil {
		return s.failJob(ctx, job, errs.New(errs.CodeInternal, "写入导出对象失败").WithCause(err))
	}
	downloadURL, expiresAt, err := s.deps.Objects.PresignGet(ctx, objectKey, entity.ExportDownloadTTL)
	if err != nil {
		return s.failJob(ctx, job, errs.New(errs.CodeInternal, "签发下载地址失败").WithCause(err))
	}

	job.Succeed(objectKey, expiresAt, s.clock.Now())
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ExportView{}, err
	}
	return toView(job, downloadURL), nil
}

/* generate 按格式生成归档字节与 Content-Type */
func (s *Service) generate(ctx context.Context, userID, noteID, folderID string,
	format entity.ExportFormat) ([]byte, string, error) {
	if format == entity.ExportFormatPDF {
		data, err := s.buildPDF(ctx, userID, noteID)
		return data, "application/pdf", err
	}
	data, err := s.buildMarkdownArchive(ctx, userID, noteID, folderID)
	return data, "application/zip", err
}

/* failJob 记录失败终态并返回视图；任务写入失败才把错误上抛 */
func (s *Service) failJob(ctx context.Context, job *entity.ExportJob, cause error) (ExportView, error) {
	job.Fail(failureCode(cause), s.clock.Now())
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ExportView{}, err
	}
	return toView(job, ""), nil
}
