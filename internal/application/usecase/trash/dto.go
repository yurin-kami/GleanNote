/* 回收站用例的输入输出类型；HTTP DTO 由传输层独立定义，避免协议耦合进用例 */
package trash

import "time"

/* ListTrashInput 是回收站分页查询 */
type ListTrashInput struct {
	UserID    string
	PageSize  int
	PageToken string
}

/* TrashItemView 是一个删除批次的聚合视图，字段与 TrashEntry 对齐 */
type TrashItemView struct {
	BatchID       string
	ItemType      string
	ItemID        string
	Name          string
	OriginalPath  string
	FolderCount   int
	NoteCount     int
	DeletedAt     time.Time
	PurgeAt       time.Time
	RemainingDays int
}

/* ListTrashResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListTrashResult struct {
	Items         []TrashItemView
	NextPageToken string
}
