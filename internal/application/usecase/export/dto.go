/* 导出用例的输入输出类型；HTTP DTO 由传输层独立定义 */
package export

import "time"

/* CreateExportInput 是发起导出的入参；NoteID 与 FolderID 二选一 */
type CreateExportInput struct {
	UserID   string
	NoteID   string
	FolderID string
	Format   string
}

/* ExportView 是导出任务对外视图；DownloadURL 仅在成功态回填 */
type ExportView struct {
	ID          string
	State       string
	DownloadURL string
	ExpiresAt   *time.Time
	FailureCode string
	CreatedAt   time.Time
	FinishedAt  *time.Time
}
