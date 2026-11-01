/* 批量导入用例的输入输出类型；HTTP DTO 由传输层独立定义 */
package imports

/* CreateImportInput 是发起批量导入的入参；UploadIDs 顺序即导入顺序 */
type CreateImportInput struct {
	UserID    string
	FolderID  string
	Mode      string
	UploadIDs []string
}

/* ImportItemView 是单个上传会话的导入结果 */
type ImportItemView struct {
	UploadID    string
	NoteID      string
	State       string
	FailureCode string
}

/* ImportView 是一次批量导入的对外视图 */
type ImportView struct {
	ID        string
	State     string
	Total     int
	Succeeded int
	Failed    int
	Items     []ImportItemView
}
