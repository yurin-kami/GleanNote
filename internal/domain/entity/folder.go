/* 目录实体：树结构、改名、回收站状态与根目录构造 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * 目录相关常量。
 * MaxFolderDepth 与 RootFolderName 的口径见 docs/API.md#523-post-v1folders
 * 与 PRD FR-01：注册成功必须自动创建名为「我的笔记」的第 1 层目录。
 */
const (
	MaxFolderDepth = 5
	RootFolderName = "我的笔记"
)

/*
 * Folder 是目录树的节点。
 * Path 采用物化路径（/{rootId}/{childId}/...），使面包屑与子树查询退化为前缀匹配。
 */
type Folder struct {
	ID            string
	UserID        string
	ParentID      *string
	Name          string
	Depth         int
	Path          string
	Revision      int
	SortOrder     int
	DeletedAt     *time.Time
	DeleteBatchID string
	PurgeAt       *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

/*
 * NewRootFolder 构造账号根目录。
 * 约束：depth=1、parent 为空、path 为 /{id}、revision=1，供注册事务与仓储直接落库。
 */
func NewRootFolder(id, userID string, now time.Time) *Folder {
	return &Folder{
		ID:        id,
		UserID:    userID,
		Name:      RootFolderName,
		Depth:     1,
		Path:      "/" + id,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

/*
 * NewFolder 构造新建目录。
 * parent 为 nil 表示根级（depth=1、path=/{id}）；否则继承父路径并加深一层。
 */
func NewFolder(id, userID string, parent *Folder, name valueobject.FolderName, now time.Time) *Folder {
	folder := &Folder{
		ID:        id,
		UserID:    userID,
		Name:      name.String(),
		Depth:     1,
		Path:      "/" + id,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if parent != nil {
		parentID := parent.ID
		folder.ParentID = &parentID
		folder.Depth = parent.Depth + 1
		folder.Path = parent.Path + "/" + id
	}
	return folder
}

/* IsDeleted 判断节点是否处于回收站 */
func (f *Folder) IsDeleted() bool { return f.DeletedAt != nil }

/*
 * Rename 按修订号做乐观锁更新名称。
 * 修订号不匹配返回 REVISION_MISMATCH；成功时修订号自增并刷新更新时间。
 */
func (f *Folder) Rename(name valueobject.FolderName, revision int, now time.Time) error {
	if f.Revision != revision {
		return errs.New(errs.CodeRevisionMismatch, "目录版本已变化")
	}
	f.Name = name.String()
	f.Revision++
	f.UpdatedAt = now
	return nil
}

/*
 * MarkDeleted 把节点移入回收站。
 * 已删除节点保持首次删除的批次与到期时间，避免级联删除改写原回收站记录。
 */
func (f *Folder) MarkDeleted(batchID string, now time.Time, retention time.Duration) {
	if f.IsDeleted() {
		return
	}
	deletedAt := now
	purgeAt := now.Add(retention)
	f.DeletedAt = &deletedAt
	f.PurgeAt = &purgeAt
	f.DeleteBatchID = batchID
	f.UpdatedAt = now
}

/*
 * Restore 恢复节点并落在调用方给定的父级、深度与路径上。
 * 名称与修订号由调用方按重名裁决结果单独处理。
 */
func (f *Folder) Restore(parentID *string, depth int, path string, now time.Time) {
	f.ParentID = parentID
	f.Depth = depth
	f.Path = path
	f.DeletedAt = nil
	f.PurgeAt = nil
	f.DeleteBatchID = ""
	f.UpdatedAt = now
}
