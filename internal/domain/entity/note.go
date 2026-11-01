/* 笔记实体：状态机、乐观锁保存、回收站与冲突副本构造 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* NoteState 是笔记状态，取值与 docs/API.md#32-枚举字典 一致 */
type NoteState string

/*
 * 笔记状态常量。
 * DRAFT 由转换流程创建；NORMAL 为可编辑正常态；DELETED 仅出现在回收站与增量同步。
 */
const (
	NoteStateDraft   NoteState = "DRAFT"
	NoteStateNormal  NoteState = "NORMAL"
	NoteStateDeleted NoteState = "DELETED"
)

/*
 * Note 是一篇笔记的聚合。
 * 技术字段：Revision 用于乐观锁，CharCount 供列表展示与容量校验，ConflictCopy 标记冲突副本。
 */
type Note struct {
	ID            string
	UserID        string
	FolderID      string
	Title         string
	Content       string
	State         NoteState
	Revision      int
	CharCount     int
	ConflictCopy  bool
	DeletedAt     *time.Time
	DeleteBatchID string
	PurgeAt       *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

/*
 * NewNote 构造新建空白笔记。
 * 约束：state=NORMAL、revision=1、正文字符数由内容值对象计算；createdAt 允许客户端离线时间。
 * 参考：docs/API.md#533-post-v1notes
 */
func NewNote(id, userID, folderID string, title valueobject.NoteTitle,
	content valueobject.NoteContent, createdAt, now time.Time) *Note {
	return &Note{
		ID:        id,
		UserID:    userID,
		FolderID:  folderID,
		Title:     title.String(),
		Content:   content.String(),
		State:     NoteStateNormal,
		Revision:  1,
		CharCount: content.CharCount(),
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
}

/*
 * NewDraftNote 构造由图片创建、等待转换的草稿笔记。
 * 约束：state=DRAFT、正文为空、revision=1；标题由转换结果回填，此前展示「未命名笔记」。
 * 参考：docs/API.md#534-post-v1notescreatefrompages、PRD FR-05
 */
func NewDraftNote(id, userID, folderID string, title valueobject.NoteTitle, now time.Time) *Note {
	return &Note{
		ID:        id,
		UserID:    userID,
		FolderID:  folderID,
		Title:     title.String(),
		State:     NoteStateDraft,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

/*
 * NewConflictCopy 以当前正文构造冲突副本笔记。
 * 约束：副本独立成篇、state=NORMAL、revision=1，并标记 ConflictCopy 以便客户端提示。
 * 参考：docs/ARCHITECTURE.md#63-写冲突与离线编辑
 */
func NewConflictCopy(id, userID, folderID string, title valueobject.NoteTitle,
	content valueobject.NoteContent, now time.Time) *Note {
	note := NewNote(id, userID, folderID, title, content, now, now)
	note.ConflictCopy = true
	return note
}

/* IsDeleted 判断笔记是否处于回收站 */
func (n *Note) IsDeleted() bool { return n.DeletedAt != nil }

/* DisplayTitle 返回展示标题，空标题回落到「未命名笔记」 */
func (n *Note) DisplayTitle() string {
	return valueobject.NoteTitle(n.Title).Display()
}

/* Editable 校验当前状态是否允许编辑或移动；DELETED 返回 NOTE_STATE_INVALID */
func (n *Note) Editable() error {
	if n.State == NoteStateDraft || n.State == NoteStateNormal {
		return nil
	}
	return errs.New(errs.CodeNoteStateInvalid, "已删除的笔记不可编辑")
}

/*
 * Save 覆盖标题与正文并推进修订号。
 * 规则：DRAFT 首次保存自动转为 NORMAL；状态非法返回 NOTE_STATE_INVALID。
 * 参考：docs/ARCHITECTURE.md#54-领域对象与状态机、docs/API.md#536-patch-v1notesnoteid
 */
func (n *Note) Save(title valueobject.NoteTitle, content valueobject.NoteContent, now time.Time) error {
	if err := n.Editable(); err != nil {
		return err
	}
	n.Title = title.String()
	n.Content = content.String()
	n.CharCount = content.CharCount()
	n.State = NoteStateNormal
	n.Revision++
	n.UpdatedAt = now
	return nil
}

/* Move 把笔记移动到目标目录；不改正文与修订号，只认状态是否可编辑 */
func (n *Note) Move(folderID string, now time.Time) error {
	if err := n.Editable(); err != nil {
		return err
	}
	n.FolderID = folderID
	n.UpdatedAt = now
	return nil
}

/*
 * MarkDeleted 把笔记移入回收站。
 * 已删除笔记保持首次删除的批次与到期时间，避免重复删除改写回收站记录。
 */
func (n *Note) MarkDeleted(batchID string, now time.Time, retention time.Duration) {
	if n.IsDeleted() {
		return
	}
	deletedAt := now
	purgeAt := now.Add(retention)
	n.State = NoteStateDeleted
	n.DeletedAt = &deletedAt
	n.PurgeAt = &purgeAt
	n.DeleteBatchID = batchID
	n.UpdatedAt = now
}

/*
 * Restore 恢复笔记并落到调用方给定的目录。
 * 规则：恢复后固定为 NORMAL；分享链接不回滚，保持失效（PRD 5.4）。
 * 参考：docs/ARCHITECTURE.md#64-回收站恢复与到期清理
 */
func (n *Note) Restore(folderID string, now time.Time) {
	n.FolderID = folderID
	n.State = NoteStateNormal
	n.DeletedAt = nil
	n.PurgeAt = nil
	n.DeleteBatchID = ""
	n.UpdatedAt = now
}
