/* 增量同步的查询入参与视图类型 */
package changes

import "time"

/* ListChangesInput 描述一次增量拉取；Since 为上次响应的 nextCursor */
type ListChangesInput struct {
	UserID        string
	Since         int64
	PageSize      int
	ResourceTypes []string
}

/* ChangeView 是一条变更；DELETE 事件的 Payload 为 nil */
type ChangeView struct {
	Seq          int64
	ResourceType string
	ResourceID   string
	Op           string
	Revision     int
	UpdatedAt    time.Time
	Payload      map[string]any
}

/* ListChangesResult 返回变更集与下一游标；游标单调不回退 */
type ListChangesResult struct {
	Changes    []ChangeView
	NextCursor int64
	HasMore    bool
}
