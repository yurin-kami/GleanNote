/* 标签用例的输入输出类型；HTTP DTO 由传输层独立定义，避免协议耦合进用例 */
package tag

import "time"

/* TagView 是标签对外的稳定视图，NoteCount 只统计未删除笔记 */
type TagView struct {
	ID        string
	Name      string
	NoteCount int
	CreatedAt time.Time
}

/* ListTagsInput 是标签列表查询；OrderBy 为空时按笔记数倒序 */
type ListTagsInput struct {
	UserID    string
	OrderBy   string
	PageToken string
	PageSize  int
}

/* ListTagsResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListTagsResult struct {
	Items         []TagView
	NextPageToken string
	TotalSize     int
}

/* CreateTagInput 是新建标签入参；名称唯一性由用例校验 */
type CreateTagInput struct {
	UserID string
	Name   string
}

/* RenameTagInput 是标签重命名入参；关联笔记无需级联更新 */
type RenameTagInput struct {
	UserID string
	TagID  string
	Name   string
}

/* SetNoteTagsInput 整体覆盖一篇笔记的标签；TagIDs 顺序即返回顺序 */
type SetNoteTagsInput struct {
	UserID string
	NoteID string
	TagIDs []string
}

/* SetNoteTagsResult 汇总覆盖结果；Items 为覆盖后的标签列表 */
type SetNoteTagsResult struct {
	Items []TagView
}
