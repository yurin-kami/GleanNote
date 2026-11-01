/*
 * 目录模块的持久化端口。
 *
 * 职责：以领域实体为边界描述目录树读写，隐藏索引与事务实现。
 * 不负责：业务判断（重名、深度、环检测由用例与领域负责）。
 * 约束：所有方法必须携带 userID，跨用户访问一律按 RESOURCE_NOT_FOUND 处理（G-08）。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/API.md#52-目录
 */
package port

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* FolderOrder 是目录列表允许的排序方式，取值与契约一致 */
type FolderOrder string

/* 排序白名单：创建时间升序（默认）与名称升序 */
const (
	FolderOrderCreatedAtAsc FolderOrder = "createTime asc"
	FolderOrderNameAsc      FolderOrder = "name asc"
)

/*
 * FolderSortKey 返回排序键，供仓储排序与传输层生成分页令牌共用。
 * 名称排序使用小写形式，与数据库 lower(name) 唯一索引口径一致。
 */
func FolderSortKey(order FolderOrder, folder *entity.Folder) string {
	if order == FolderOrderNameAsc {
		return strings.ToLower(folder.Name)
	}
	return folder.CreatedAt.UTC().Format(time.RFC3339Nano)
}

/* FolderCursor 是键集分页位置，排序键与 ID 共同保证翻页稳定 */
type FolderCursor struct {
	SortKey string
	ID      string
}

/* FolderListQuery 描述一次子目录分页查询 */
type FolderListQuery struct {
	UserID   string
	ParentID *string
	Order    FolderOrder
	Limit    int
	After    *FolderCursor
}

/*
 * FolderRepository 管理目录树。
 * 约定：Create 遇到同级重名返回 NAME_DUPLICATED；GetByID 返回包含已删除节点的原始行，
 * 由用例决定可见性；ListChildren 只返回未删除节点。
 */
type FolderRepository interface {
	Create(ctx context.Context, folder *entity.Folder) error
	GetByID(ctx context.Context, userID, folderID string) (*entity.Folder, error)
	ListChildren(ctx context.Context, query FolderListQuery) (items []*entity.Folder, totalSize int, err error)
	CountChildren(ctx context.Context, userID string, parentIDs []string) (map[string]int, error)
	NameExists(ctx context.Context, userID string, parentID *string, name, excludeID string) (bool, error)
	/*
	 * LockTree 在事务内以 pg_advisory_xact_lock 串行化同一账号的目录树写入。
	 * 移动与批量重写物化路径必须先取树锁，否则并发改名/移动会产生断裂路径（ARCHITECTURE 6.3）。
	 * 内存实现为空操作（仓储已用全局锁串行化）。
	 */
	LockTree(ctx context.Context, userID string) error
	Update(ctx context.Context, folder *entity.Folder) error
	/*
	 * UpdateWithRevision 以乐观锁写入：仅当数据库当前 revision 等于 expected 时更新。
	 * 返回 false 表示版本已变化且未写入任何行。
	 */
	UpdateWithRevision(ctx context.Context, folder *entity.Folder, expected int) (bool, error)
	ListSubtree(ctx context.Context, userID, folderID string) ([]*entity.Folder, error)
	ListDeleted(ctx context.Context, userID string) ([]*entity.Folder, error)
	/* ListExpired 返回 purge_at 已到期、等待定时清理的回收站目录 */
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Folder, error)
	Root(ctx context.Context, userID string) (*entity.Folder, error)
	SaveBatch(ctx context.Context, folders []*entity.Folder) error
	DeleteBatch(ctx context.Context, userID, folderID string) (int, error)
	DeleteAllByUser(ctx context.Context, userID string) error
}
