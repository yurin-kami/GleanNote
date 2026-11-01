/*
 * 导出任务实体：格式、状态机与下载地址有效期。
 *
 * 职责：固化导出目标（单篇或目录）与生成结果，约束状态迁移。
 * 不负责：归档生成、对象存储写入与文件名规则（由用例与适配器负责）。
 * 参考：docs/API.md#511-导出与导入（p1）、PRD FR-14
 */
package entity

import "time"

/* ExportFormat 是导出格式，取值与契约一致 */
type ExportFormat string

/* 导出格式：Markdown 压缩包与 PDF */
const (
	ExportFormatMarkdownZip ExportFormat = "MARKDOWN_ZIP"
	ExportFormatPDF         ExportFormat = "PDF"
)

/* ExportState 是导出任务状态 */
type ExportState string

/* 导出状态：排队、处理中、成功与失败 */
const (
	ExportStateQueued    ExportState = "QUEUED"
	ExportStateRunning   ExportState = "RUNNING"
	ExportStateSucceeded ExportState = "SUCCEEDED"
	ExportStateFailed    ExportState = "FAILED"
)

/* ExportDownloadTTL 是下载地址有效期（PRD FR-14、docs/API.md#511） */
const ExportDownloadTTL = time.Hour

/*
 * ExportJob 是一次导出任务。
 * 约束：NoteID 与 FolderID 二选一；成功后必须写入 ObjectKey 与 ExpiresAt。
 */
type ExportJob struct {
	ID          string
	UserID      string
	NoteID      string
	FolderID    string
	Format      ExportFormat
	State       ExportState
	ObjectKey   string
	FailureCode string
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

/* NewExportJob 创建排队中的导出任务 */
func NewExportJob(id, userID, noteID, folderID string, format ExportFormat, now time.Time) *ExportJob {
	return &ExportJob{
		ID:        id,
		UserID:    userID,
		NoteID:    noteID,
		FolderID:  folderID,
		Format:    format,
		State:     ExportStateQueued,
		CreatedAt: now,
	}
}

/* MarkRunning 把任务置为处理中；仅排队态可进入 */
func (j *ExportJob) MarkRunning() {
	j.State = ExportStateRunning
}

/* Succeed 记录生成结果与下载有效期 */
func (j *ExportJob) Succeed(objectKey string, expiresAt, now time.Time) {
	j.State = ExportStateSucceeded
	j.ObjectKey = objectKey
	j.ExpiresAt = &expiresAt
	j.FailureCode = ""
	j.FinishedAt = &now
}

/* Fail 记录失败原因；失败任务不产生下载地址 */
func (j *ExportJob) Fail(code string, now time.Time) {
	j.State = ExportStateFailed
	j.FailureCode = code
	j.ObjectKey = ""
	j.ExpiresAt = nil
	j.FinishedAt = &now
}
