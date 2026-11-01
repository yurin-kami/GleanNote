/* 笔记用例的输入输出类型；HTTP DTO 由传输层独立定义，避免协议耦合进用例 */
package note

import "time"

/* ListNotesInput 是目录内笔记查询；State 为空表示排除回收站 */
type ListNotesInput struct {
	UserID       string
	FolderID     string
	State        string
	UpdatedAfter string
	OrderBy      string
	PageToken    string
	PageSize     int
}

/* CreateNoteInput 是新建空白笔记入参；ClientCreatedAt 用于离线补录排序纠偏 */
type CreateNoteInput struct {
	UserID          string
	FolderID        string
	Title           string
	Content         string
	ClientCreatedAt string
}

/* UpdateNoteInput 携带乐观锁修订号；Title/Content 为 nil 表示本次不修改该字段 */
type UpdateNoteInput struct {
	UserID     string
	NoteID     string
	Title      *string
	Content    *string
	Revision   int
	SaveSource string
}

/* CreateFromPagesInput 由已上传图片创建草稿；UploadIDs 顺序即页面顺序 */
type CreateFromPagesInput struct {
	UserID    string
	FolderID  string
	UploadIDs []string
	Title     string
}

/* AppendPagesInput 向已有笔记追加页面；UploadIDs 顺序即新增页面顺序 */
type AppendPagesInput struct {
	UserID    string
	NoteID    string
	UploadIDs []string
}

/* AppendPagesResult 返回追加任务与扣减后的额度快照 */
type AppendPagesResult struct {
	Job   ConversionJobView
	Quota QuotaView
}

/* ConversionJobView 是提交响应中的任务摘要，完整任务视图由转换模块提供 */
type ConversionJobView struct {
	ID             string
	State          string
	Phase          string
	TotalPages     int
	ProcessedPages int
}

/* QuotaView 是提交后的当日额度快照；Charged 为本次扣减张数 */
type QuotaView struct {
	Remaining int
	Charged   int
}

/* CreateFromPagesResult 汇总创建结果：笔记、任务与额度 */
type CreateFromPagesResult struct {
	Note  NoteView
	Job   ConversionJobView
	Quota QuotaView
}

/* MoveNoteInput 把笔记移动到目标目录 */
type MoveNoteInput struct {
	UserID   string
	NoteID   string
	FolderID string
}

/* TagView 是笔记标签的最小视图；P1 标签模块落地前保持为空 */
type TagView struct {
	ID   string
	Name string
}

/* NoteView 是笔记对外的稳定视图；列表场景不输出 Content 与 ShareEnabled */
type NoteView struct {
	ID             string
	FolderID       string
	FolderPath     string
	Title          string
	State          string
	Revision       int
	CharCount      int
	PageCount      int
	ThumbnailURL   string
	Tags           []TagView
	IsConflictCopy bool
	Content        string
	ShareEnabled   bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

/* ListNotesResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListNotesResult struct {
	Items         []NoteView
	NextPageToken string
	TotalSize     int
}

/* SaveNoteResult 汇总保存结果；发生冲突副本时 ConflictCopyID 非空 */
type SaveNoteResult struct {
	Note           NoteView
	ConflictCopyID string
}

/* DeleteNoteResult 是软删除后的回收站记录摘要 */
type DeleteNoteResult struct {
	DeleteBatchID string
	PurgeAt       time.Time
}

/* RestoreNoteResult 汇总恢复结果；RestoredToRoot 表示原目录不可用而回落根级 */
type RestoreNoteResult struct {
	Note           NoteView
	RestoredToRoot bool
}

/* SearchNotesInput 是全文搜索入参；FolderID 含子树，TagIDs 语义为同时包含 */
type SearchNotesInput struct {
	UserID    string
	Query     string
	FolderID  string
	TagIDs    []string
	PageToken string
	PageSize  int
}

/* SearchItem 是单条搜索结果；Snippet 仅允许 <em> 一种标记 */
type SearchItem struct {
	NoteID     string
	Title      string
	Snippet    string
	FolderPath string
	UpdatedAt  time.Time
}

/* SearchNotesResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type SearchNotesResult struct {
	Items         []SearchItem
	NextPageToken string
	TotalSize     int
}

/* FolderRestore 是目录恢复结果，只暴露笔记用例需要的最小信息 */
type FolderRestore struct {
	FolderID       string
	RestoredToRoot bool
}
