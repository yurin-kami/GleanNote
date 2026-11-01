/*
 * 批量导入实体：导入模式、整体状态与逐项状态。
 *
 * 职责：承载一次批量导入的目标目录、模式、上传会话清单与逐项结果。
 * 不负责：图片上传、笔记创建与转换提交（由用例编排），也不负责任务取消。
 * 参考：docs/API.md#511-导出与导入（p1）、PRD FR-17
 */
package entity

import "time"

/* ImportMode 是批量导入模式，取值与契约一致 */
type ImportMode string

/* 导入模式：每张图片一篇笔记，或按选择顺序合并为一篇 */
const (
	ImportModePerImage ImportMode = "PER_IMAGE"
	ImportModeMerge    ImportMode = "MERGE"
)

/* ImportState 是导入任务整体状态，取值与契约一致 */
type ImportState string

/* 导入状态：排队、处理中、全部成功、部分失败、全部失败与已取消 */
const (
	ImportStateQueued        ImportState = "QUEUED"
	ImportStateRunning       ImportState = "RUNNING"
	ImportStateSucceeded     ImportState = "SUCCEEDED"
	ImportStatePartialFailed ImportState = "PARTIAL_FAILED"
	ImportStateFailed        ImportState = "FAILED"
	ImportStateCanceled      ImportState = "CANCELED"
)

/* ImportItemState 是单个导入项的状态；成功表示草稿已创建且转换任务已提交 */
type ImportItemState string

/* 导入项状态常量 */
const (
	ImportItemQueued    ImportItemState = "QUEUED"
	ImportItemSucceeded ImportItemState = "SUCCEEDED"
	ImportItemFailed    ImportItemState = "FAILED"
)

/*
 * ImportItem 是一个上传会话的导入结果。
 * 约束：MERGE 模式下同一篇笔记的多个导入项 NoteID 相同。
 */
type ImportItem struct {
	UploadID    string
	NoteID      string
	State       ImportItemState
	FailureCode string
}

/* ImportJob 是一次批量导入任务 */
type ImportJob struct {
	ID          string
	UserID      string
	FolderID    string
	Mode        ImportMode
	State       ImportState
	Total       int
	Succeeded   int
	Failed      int
	Items       []*ImportItem
	FailureCode string
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

/*
 * NewImportJob 创建排队中的导入任务并生成逐项占位记录。
 * 约束：uploadIDs 顺序即导入顺序，重复项由用例提前拒绝。
 */
func NewImportJob(id, userID, folderID string, mode ImportMode, uploadIDs []string, now time.Time) *ImportJob {
	items := make([]*ImportItem, 0, len(uploadIDs))
	for _, uploadID := range uploadIDs {
		items = append(items, &ImportItem{UploadID: uploadID, State: ImportItemQueued})
	}
	return &ImportJob{
		ID:        id,
		UserID:    userID,
		FolderID:  folderID,
		Mode:      mode,
		State:     ImportStateQueued,
		Total:     len(items),
		Items:     items,
		CreatedAt: now,
	}
}

/* MarkRunning 把任务置为处理中；仅排队态可进入 */
func (j *ImportJob) MarkRunning() {
	j.State = ImportStateRunning
}

/* SucceedItem 记录单项成功：草稿已创建，转换任务已提交 */
func (j *ImportJob) SucceedItem(index int, noteID string) {
	j.Items[index].NoteID = noteID
	j.Items[index].State = ImportItemSucceeded
	j.Items[index].FailureCode = ""
}

/* FailItem 记录单项失败原因；失败项不产生笔记 */
func (j *ImportJob) FailItem(index int, failureCode string) {
	j.Items[index].NoteID = ""
	j.Items[index].State = ImportItemFailed
	j.Items[index].FailureCode = failureCode
}

/*
 * Finish 汇总逐项结果并落定整体状态。
 * 规则：全部成功为 SUCCEEDED；全部失败为 FAILED；其余为 PARTIAL_FAILED（PRD FR-17）。
 */
func (j *ImportJob) Finish(now time.Time) {
	succeeded, failed := 0, 0
	for _, item := range j.Items {
		switch item.State {
		case ImportItemSucceeded:
			succeeded++
		case ImportItemFailed:
			failed++
		}
	}
	j.Succeeded = succeeded
	j.Failed = failed
	switch {
	case failed == 0:
		j.State = ImportStateSucceeded
	case succeeded == 0:
		j.State = ImportStateFailed
	default:
		j.State = ImportStatePartialFailed
	}
	j.FinishedAt = &now
}
