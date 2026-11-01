/* 导出用例的共享辅助：格式解析、可导出校验、文件名与失败码映射 */
package export

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* parseFormat 校验收到的导出格式，大小写不敏感 */
func parseFormat(raw string) (entity.ExportFormat, error) {
	switch entity.ExportFormat(strings.ToUpper(strings.TrimSpace(raw))) {
	case entity.ExportFormatMarkdownZip:
		return entity.ExportFormatMarkdownZip, nil
	case entity.ExportFormatPDF:
		return entity.ExportFormatPDF, nil
	default:
		return "", errs.New(errs.CodeValidationFailed, "format 必须为 MARKDOWN_ZIP 或 PDF")
	}
}

/*
 * loadExportableNote 读取可导出笔记。
 * 规则：回收站笔记按不存在处理；DRAFT 等非 NORMAL 状态返回 NOTE_STATE_INVALID（PRD FR-14）。
 */
func (s *Service) loadExportableNote(ctx context.Context, userID, noteID string) (*entity.Note, error) {
	note, err := s.deps.Notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.IsDeleted() {
		return nil, errs.New(errs.CodeResourceNotFound, "笔记不存在")
	}
	if note.State != entity.NoteStateNormal {
		return nil, errs.New(errs.CodeNoteStateInvalid, "草稿需完成转换后才能导出")
	}
	return note, nil
}

/* loadActiveFolder 读取未删除目录；回收站节点按不存在处理 */
func (s *Service) loadActiveFolder(ctx context.Context, userID, folderID string) (*entity.Folder, error) {
	folder, err := s.deps.Folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}
	if folder.IsDeleted() {
		return nil, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}
	return folder, nil
}

/* toView 把导出任务映射为对外视图；下载地址由调用方按需签发 */
func toView(job *entity.ExportJob, downloadURL string) ExportView {
	return ExportView{
		ID:          job.ID,
		State:       string(job.State),
		DownloadURL: downloadURL,
		ExpiresAt:   job.ExpiresAt,
		FailureCode: job.FailureCode,
		CreatedAt:   job.CreatedAt,
		FinishedAt:  job.FinishedAt,
	}
}

/* failureCode 把错误收敛为任务失败码；非业务错误统一记为 INTERNAL */
func failureCode(err error) string {
	if code, ok := errs.CodeOf(err); ok {
		return string(code)
	}
	return string(errs.CodeInternal)
}

/* formatExtension 返回导出对象的扩展名 */
func formatExtension(format entity.ExportFormat) string {
	if format == entity.ExportFormatPDF {
		return "pdf"
	}
	return "zip"
}

/* imageExtension 依据对象 Content-Type 推导图片扩展名，未知类型回落到 jpg */
func imageExtension(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}

/* isJPEG 判断对象是否可直接嵌入 PDF（DCTDecode 仅支持 JPEG） */
func isJPEG(contentType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return normalized == "image/jpeg" || normalized == "image/jpg"
}
