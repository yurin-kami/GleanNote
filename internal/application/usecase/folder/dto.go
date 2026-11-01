/* 目录用例的输入输出类型；HTTP DTO 由传输层独立定义，避免协议耦合进用例 */
package folder

import "time"

/* ListFoldersInput 是子目录列表查询；ParentID 为空表示根级 */
type ListFoldersInput struct {
	UserID    string
	ParentID  string
	PageToken string
	OrderBy   string
	PageSize  int
}

/* CreateFolderInput 是新建目录入参；ParentID 为空表示根级 */
type CreateFolderInput struct {
	UserID   string
	ParentID string
	Name     string
}

/* RenameFolderInput 携带乐观锁修订号 */
type RenameFolderInput struct {
	UserID   string
	FolderID string
	Name     string
	Revision int
}

/* MoveFolderInput 的 TargetParentID 为空表示移到根级 */
type MoveFolderInput struct {
	UserID         string
	FolderID       string
	TargetParentID string
}

/* FolderView 是目录对外的稳定视图；ChildCount 由调用方按需填充 */
type FolderView struct {
	ID             string
	ParentID       *string
	Name           string
	Depth          int
	Revision       int
	ChildCount     int
	NoteCount      int
	RestoredToRoot bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

/* FolderPathItem 是面包屑的一段 */
type FolderPathItem struct {
	ID   string
	Name string
}

/* FolderPathView 是面包屑与原路径展示 */
type FolderPathView struct {
	Items       []FolderPathItem
	DisplayPath string
}

/* ListFoldersResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListFoldersResult struct {
	Items         []FolderView
	NextPageToken string
	TotalSize     int
}

/* MoveResult 汇总移动后的目录与受影响的目录与笔记数量 */
type MoveResult struct {
	Folder           FolderView
	MovedFolderCount int
	MovedNoteCount   int
}

/* DeleteResult 是整棵子树软删除后的回收站记录摘要 */
type DeleteResult struct {
	DeleteBatchID string
	FolderCount   int
	NoteCount     int
	PurgeAt       time.Time
}
