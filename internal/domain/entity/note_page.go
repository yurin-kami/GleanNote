/*
 * 笔记页面实体：一张已校验原图及其缩略图在笔记中的有序位置。
 *
 * 职责：固化页面顺序（从 1 开始）、原图/缩略图对象键与尺寸，作为转换任务输入。
 * 不负责：上传会话生命周期、对象清理与容量累计（由用例与端口协作）。
 * 依赖：标准库、上传实体。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/API.md#543-get-v1notesnoteidpages
 */
package entity

import "time"

/* NotePage 是笔记内的一页；Index 即用户排序，唯一且从 1 开始 */
type NotePage struct {
	ID        string
	NoteID    string
	UserID    string
	Index     int
	ObjectKey string
	ThumbKey  string
	Bytes     int64
	Width     int
	Height    int
	SHA256    string
	CreatedAt time.Time
}

/*
 * NewNotePage 从已完成校验的上传会话生成页面。
 * 约束：index 从 1 开始；原图与缩略图对象键直接继承上传会话，不复制对象。
 */
func NewNotePage(id, noteID, userID string, index int, upload *Upload, now time.Time) *NotePage {
	return &NotePage{
		ID:        id,
		NoteID:    noteID,
		UserID:    userID,
		Index:     index,
		ObjectKey: upload.ObjectKey,
		ThumbKey:  upload.ThumbKey,
		Bytes:     upload.Bytes,
		Width:     upload.Width,
		Height:    upload.Height,
		SHA256:    upload.SHA256,
		CreatedAt: now,
	}
}
