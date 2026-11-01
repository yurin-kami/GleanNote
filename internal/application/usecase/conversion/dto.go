/* 转换任务的对外视图；失败码为空表示非失败态 */
package conversion

import "time"

/* JobView 是任务对象，与 docs/API.md#551-任务对象 的字段一一对应 */
type JobView struct {
	ID                 string
	NoteID             string
	Kind               string
	State              string
	Phase              string
	TotalPages         int
	ProcessedPages     int
	FailureCode        string
	FailureUserMessage string
	QuotaCharged       int
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}
