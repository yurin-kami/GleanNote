/*
 * 笔记模块的持久化端口。
 *
 * 职责：以领域实体为边界描述笔记读写、分页与按目录批处理，隐藏索引与事务实现。
 * 不负责：冲突裁决、状态校验与目录可见性判断（由用例与领域负责）。
 * 约束：所有方法必须携带 userID，跨用户访问一律按 RESOURCE_NOT_FOUND 处理（G-08）。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/API.md#53-笔记
 */
package port

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* NoteOrder 是笔记列表允许的排序方式，取值与契约一致 */
type NoteOrder string

/* 排序白名单：更新时间倒序（默认）、创建时间倒序与标题升序 */
const (
	NoteOrderUpdatedDesc NoteOrder = "updateTime desc"
	NoteOrderCreatedDesc NoteOrder = "createTime desc"
	NoteOrderTitleAsc    NoteOrder = "title asc"
)

/*
 * NoteSortKey 返回升序可比的排序键，供仓储排序与分页令牌共用。
 * 倒序时间用「最大值减纳秒」翻转，使键集分页只需升序比较；标题使用小写形式。
 */
func NoteSortKey(order NoteOrder, note *entity.Note) string {
	switch order {
	case NoteOrderCreatedDesc:
		return fmt.Sprintf("%019d", math.MaxInt64-note.CreatedAt.UnixNano())
	case NoteOrderTitleAsc:
		return strings.ToLower(note.Title)
	default:
		return fmt.Sprintf("%019d", math.MaxInt64-note.UpdatedAt.UnixNano())
	}
}

/* NoteCursor 是键集分页位置，排序键与 ID 共同保证翻页稳定 */
type NoteCursor struct {
	SortKey string
	ID      string
}

/*
 * NoteSearchQuery 描述一次全文搜索。
 * FolderIDs 为空表示不限目录（含子树由用例解析）；NoteIDs 为空表示不限标签命中集合。
 * 命中层级由仓储按标题 / 正文计算，排序口径与契约一致（docs/API.md#5311-get-v1notessearch）。
 */
type NoteSearchQuery struct {
	UserID    string
	Keyword   string
	FolderIDs []string
	NoteIDs   []string
	Limit     int
	After     *NoteCursor
}

/*
 * NoteSearchSortKey 把命中层级与更新时间编码为升序键。
 * tier=0 表示标题命中、tier=1 表示正文命中；同一层级内按更新时间倒序。
 */
func NoteSearchSortKey(titleMatch bool, updatedAt time.Time) string {
	tier := "1"
	if titleMatch {
		tier = "0"
	}
	return fmt.Sprintf("%s:%019d", tier, math.MaxInt64-updatedAt.UnixNano())
}

/* NoteListQuery 描述一次目录内笔记分页查询 */
type NoteListQuery struct {
	UserID       string
	FolderID     string
	State        *entity.NoteState
	UpdatedAfter *time.Time
	Order        NoteOrder
	Limit        int
	After        *NoteCursor
}

/*
 * NoteRepository 管理笔记聚合。
 * 约定：GetByID 返回包含已删除笔记的原始行，由用例决定可见性；List 按 State 过滤，
 * 未指定状态时排除 DELETED；按目录批处理的方法供目录级联删除、恢复与彻底删除使用。
 */
type NoteRepository interface {
	Create(ctx context.Context, note *entity.Note) error
	GetByID(ctx context.Context, userID, noteID string) (*entity.Note, error)
	/*
	 * GetForUpdate 在事务内锁定笔记行（SELECT ... FOR UPDATE），
	 * 保证「读取修订号 → 写入」期间不会被并发事务修改（ARCHITECTURE 6.3）。
	 */
	GetForUpdate(ctx context.Context, userID, noteID string) (*entity.Note, error)
	List(ctx context.Context, query NoteListQuery) (items []*entity.Note, totalSize int, err error)
	Search(ctx context.Context, query NoteSearchQuery) (items []*entity.Note, totalSize int, err error)
	ListDeleted(ctx context.Context, userID string) ([]*entity.Note, error)
	/* ListByFolders 返回给定目录下的全部笔记（含回收站），供目录级联删除与撤底删除使用 */
	ListByFolders(ctx context.Context, userID string, folderIDs []string) ([]*entity.Note, error)
	/* ListExpired 返回 purge_at 已到期、等待定时清理的回收站笔记 */
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Note, error)
	Update(ctx context.Context, note *entity.Note) error
	/*
	 * UpdateWithRevision 以乐观锁写入：仅当数据库当前 revision 等于 expected 时更新。
	 * 返回 false 表示并发冲突且未写入任何行，调用方必须据此报错而不能当作成功。
	 */
	UpdateWithRevision(ctx context.Context, note *entity.Note, expected int) (bool, error)
	Delete(ctx context.Context, userID, noteID string) error
	DeleteAllByUser(ctx context.Context, userID string) error
	CountActiveByFolder(ctx context.Context, userID string, folderIDs []string) (map[string]int, error)
	MarkDeletedByFolders(ctx context.Context, userID string, folderIDs []string,
		batchID string, now time.Time, retention time.Duration) (int, error)
	RestoreByBatch(ctx context.Context, userID, batchID string, now time.Time) (int, error)
}
