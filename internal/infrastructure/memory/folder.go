/* 内存目录仓储：按用户分组保存节点，支持键集分页与子树批量操作 */
package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
)

/* FolderRepository 是 port.FolderRepository 的内存实现 */
type FolderRepository struct {
	store  *Store
	byUser map[string][]*entity.Folder
}

/* NewFolderRepository 初始化按用户分组的目录集合 */
func NewFolderRepository(store *Store) *FolderRepository {
	return &FolderRepository{
		store:  store,
		byUser: make(map[string][]*entity.Folder),
	}
}

/* Create 追加目录节点并校验同级重名；ID 冲突说明生成器异常，按内部错误处理 */
func (r *FolderRepository) Create(ctx context.Context, folder *entity.Folder) error {
	release := r.store.lock(ctx)
	defer release()

	for _, existing := range r.byUser[folder.UserID] {
		if existing.ID == folder.ID {
			return errs.New(errs.CodeInternal, "目录 ID 冲突")
		}
		if !existing.IsDeleted() && sameParent(existing.ParentID, folder.ParentID) &&
			strings.EqualFold(existing.Name, folder.Name) {
			return errs.New(errs.CodeNameDuplicated, "同级目录已存在同名目录")
		}
	}
	r.byUser[folder.UserID] = append(r.byUser[folder.UserID], cloneFolder(folder))
	r.store.onRollback(ctx, func() {
		folders := r.byUser[folder.UserID]
		for index, existing := range folders {
			if existing.ID == folder.ID {
				r.byUser[folder.UserID] = append(folders[:index], folders[index+1:]...)
				return
			}
		}
	})
	r.store.recordChange(ctx, folderChangeEvent(folder, entity.ChangeOpCreate, folder.UpdatedAt))
	return nil
}

/* GetByID 返回目录原始行（含已删除节点）；未找到或不属于该用户返回 RESOURCE_NOT_FOUND */
func (r *FolderRepository) GetByID(ctx context.Context, userID, folderID string) (*entity.Folder, error) {
	release := r.store.lock(ctx)
	defer release()

	if _, folder := r.find(userID, folderID); folder == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "目录不存在")
	} else {
		return cloneFolder(folder), nil
	}
}

/* ListChildren 返回未删除的子目录；totalSize 为全部子目录数量，Limit 控制本页条数 */
func (r *FolderRepository) ListChildren(ctx context.Context, query port.FolderListQuery) ([]*entity.Folder, int, error) {
	release := r.store.lock(ctx)
	defer release()

	candidates := make([]*entity.Folder, 0)
	for _, folder := range r.byUser[query.UserID] {
		if folder.IsDeleted() || !sameParent(folder.ParentID, query.ParentID) {
			continue
		}
		candidates = append(candidates, folder)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := port.FolderSortKey(query.Order, candidates[left])
		rightKey := port.FolderSortKey(query.Order, candidates[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return candidates[left].ID < candidates[right].ID
	})
	total := len(candidates)
	if query.After != nil {
		candidates = filterAfterCursor(candidates, query)
	}
	if query.Limit > 0 && len(candidates) > query.Limit {
		candidates = candidates[:query.Limit]
	}
	return cloneFolders(candidates), total, nil
}

/* CountChildren 统计给定目录的直接子目录数量（仅未删除节点） */
func (r *FolderRepository) CountChildren(ctx context.Context, userID string, parentIDs []string) (map[string]int, error) {
	release := r.store.lock(ctx)
	defer release()

	counts := make(map[string]int, len(parentIDs))
	wanted := make(map[string]struct{}, len(parentIDs))
	for _, id := range parentIDs {
		wanted[id] = struct{}{}
	}
	for _, folder := range r.byUser[userID] {
		if folder.IsDeleted() || folder.ParentID == nil {
			continue
		}
		if _, ok := wanted[*folder.ParentID]; ok {
			counts[*folder.ParentID]++
		}
	}
	return counts, nil
}

/* NameExists 判断同级是否存在同名未删除目录，excludeID 用于改名时排除自身 */
func (r *FolderRepository) NameExists(ctx context.Context, userID string, parentID *string, name, excludeID string) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	for _, folder := range r.byUser[userID] {
		if folder.IsDeleted() || folder.ID == excludeID || !sameParent(folder.ParentID, parentID) {
			continue
		}
		if strings.EqualFold(folder.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

/* LockTree 内存实现为空操作：仓储全局锁已串行化同一账号的目录写入 */
func (r *FolderRepository) LockTree(ctx context.Context, userID string) error { return nil }

/* Update 覆盖目录行；节点不存在返回 RESOURCE_NOT_FOUND */
func (r *FolderRepository) Update(ctx context.Context, folder *entity.Folder) error {
	_, err := r.write(ctx, folder, -1)
	return err
}

/* UpdateWithRevision 以 revision 条件覆盖目录行；expected < 0 表示不做乐观锁校验 */
func (r *FolderRepository) UpdateWithRevision(ctx context.Context, folder *entity.Folder, expected int) (bool, error) {
	return r.write(ctx, folder, expected)
}

/* write 是 Update/UpdateWithRevision 的共用实现；expected 不匹配时不写任何数据 */
func (r *FolderRepository) write(ctx context.Context, folder *entity.Folder, expected int) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	index, found := r.find(folder.UserID, folder.ID)
	if found == nil {
		return false, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}
	if expected >= 0 && found.Revision != expected {
		return false, nil
	}
	previous := cloneFolder(found)
	r.byUser[folder.UserID][index] = cloneFolder(folder)
	r.store.onRollback(ctx, func() { r.byUser[folder.UserID][index] = previous })
	op := entity.ChangeOpUpdate
	if !previous.IsDeleted() && folder.IsDeleted() {
		op = entity.ChangeOpDelete
	}
	r.store.recordChange(ctx, folderChangeEvent(folder, op, folder.UpdatedAt))
	return true, nil
}

/* ListExpired 返回 purge_at 已到期的回收站目录，供定时清理任务消费 */
func (r *FolderRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Folder, error) {
	release := r.store.lock(ctx)
	defer release()

	if limit <= 0 {
		limit = 100
	}
	expired := make([]*entity.Folder, 0, limit)
	for _, folders := range r.byUser {
		for _, folder := range folders {
			if !folder.IsDeleted() || folder.PurgeAt == nil || folder.PurgeAt.After(now) {
				continue
			}
			expired = append(expired, folder)
		}
	}
	sort.SliceStable(expired, func(left, right int) bool {
		return expired[left].PurgeAt.Before(*expired[right].PurgeAt)
	})
	if len(expired) > limit {
		expired = expired[:limit]
	}
	return cloneFolders(expired), nil
}

/* ListSubtree 返回含根节点在内的整棵子树（含回收站节点），按深度与创建时间排序 */
func (r *FolderRepository) ListSubtree(ctx context.Context, userID, folderID string) ([]*entity.Folder, error) {
	release := r.store.lock(ctx)
	defer release()

	_, root := r.find(userID, folderID)
	if root == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}
	nodes := make([]*entity.Folder, 0, 8)
	for _, folder := range r.byUser[userID] {
		if service.IsWithinSubtree(root.Path, folder.Path) {
			nodes = append(nodes, folder)
		}
	}
	sort.SliceStable(nodes, func(left, right int) bool {
		if nodes[left].Depth != nodes[right].Depth {
			return nodes[left].Depth < nodes[right].Depth
		}
		if !nodes[left].CreatedAt.Equal(nodes[right].CreatedAt) {
			return nodes[left].CreatedAt.Before(nodes[right].CreatedAt)
		}
		return nodes[left].ID < nodes[right].ID
	})
	return cloneFolders(nodes), nil
}

/* ListDeleted 返回用户全部回收站目录，供回收站聚合按批次分组 */
func (r *FolderRepository) ListDeleted(ctx context.Context, userID string) ([]*entity.Folder, error) {
	release := r.store.lock(ctx)
	defer release()

	deleted := make([]*entity.Folder, 0, 8)
	for _, folder := range r.byUser[userID] {
		if folder.IsDeleted() {
			deleted = append(deleted, folder)
		}
	}
	return cloneFolders(deleted), nil
}

/* Root 返回账号根目录（parent 为空且未删除）；缺失按 RESOURCE_NOT_FOUND 处理 */
func (r *FolderRepository) Root(ctx context.Context, userID string) (*entity.Folder, error) {
	release := r.store.lock(ctx)
	defer release()

	for _, folder := range r.byUser[userID] {
		if folder.ParentID == nil && !folder.IsDeleted() {
			return cloneFolder(folder), nil
		}
	}
	return nil, errs.New(errs.CodeResourceNotFound, "账号根目录不存在")
}

/* SaveBatch 批量覆盖目录行；任一节点缺失即整体失败并回滚 */
func (r *FolderRepository) SaveBatch(ctx context.Context, folders []*entity.Folder) error {
	release := r.store.lock(ctx)
	defer release()

	for _, folder := range folders {
		index, found := r.find(folder.UserID, folder.ID)
		if found == nil {
			return errs.New(errs.CodeResourceNotFound, "目录不存在")
		}
		previous := cloneFolder(found)
		r.byUser[folder.UserID][index] = cloneFolder(folder)
		r.store.onRollback(ctx, func() { r.byUser[folder.UserID][index] = previous })
		r.store.recordChange(ctx, folderChangeEvent(folder, entity.ChangeOpUpdate, folder.UpdatedAt))
	}
	return nil
}

/* DeleteBatch 物理删除子树中与根节点同一删除批次的行，返回删除数量 */
func (r *FolderRepository) DeleteBatch(ctx context.Context, userID, folderID string) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	_, root := r.find(userID, folderID)
	if root == nil {
		return 0, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}
	batchID := root.DeleteBatchID
	previous := r.byUser[userID]
	kept := make([]*entity.Folder, 0, len(previous))
	removed := 0
	now := nowFunc()
	for _, folder := range previous {
		matchesBatch := batchID != "" && folder.DeleteBatchID == batchID
		if matchesBatch && service.IsWithinSubtree(root.Path, folder.Path) {
			r.store.recordChange(ctx, folderChangeEvent(folder, entity.ChangeOpDelete, now))
			removed++
			continue
		}
		kept = append(kept, folder)
	}
	r.byUser[userID] = kept
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	return removed, nil
}

/* DeleteAllByUser 清除账号全部目录；注销用例在一个事务内调用 */
func (r *FolderRepository) DeleteAllByUser(ctx context.Context, userID string) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byUser[userID]
	if !exists {
		return nil
	}
	delete(r.byUser, userID)
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	return nil
}

/* find 返回节点在切片中的下标与指针；不存在时 found 为 nil */
func (r *FolderRepository) find(userID, folderID string) (int, *entity.Folder) {
	for index, folder := range r.byUser[userID] {
		if folder.ID == folderID {
			return index, folder
		}
	}
	return -1, nil
}

/* sameParent 比较父级指针，nil 表示根级 */
func sameParent(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

/* filterAfterCursor 保留严格位于游标之后的节点 */
func filterAfterCursor(items []*entity.Folder, query port.FolderListQuery) []*entity.Folder {
	cursor := query.After
	filtered := make([]*entity.Folder, 0, len(items))
	for _, folder := range items {
		key := port.FolderSortKey(query.Order, folder)
		if key > cursor.SortKey || (key == cursor.SortKey && folder.ID > cursor.ID) {
			filtered = append(filtered, folder)
		}
	}
	return filtered
}

/* cloneFolders 深拷贝切片，避免调用方修改仓储内对象 */
func cloneFolders(folders []*entity.Folder) []*entity.Folder {
	if len(folders) == 0 {
		return nil
	}
	copied := make([]*entity.Folder, len(folders))
	for index, folder := range folders {
		copied[index] = cloneFolder(folder)
	}
	return copied
}

/* cloneFolder 深拷贝目录节点，避免调用方修改仓储内对象 */
func cloneFolder(folder *entity.Folder) *entity.Folder {
	if folder == nil {
		return nil
	}
	copied := *folder
	if folder.ParentID != nil {
		parent := *folder.ParentID
		copied.ParentID = &parent
	}
	if folder.DeletedAt != nil {
		deleted := *folder.DeletedAt
		copied.DeletedAt = &deleted
	}
	if folder.PurgeAt != nil {
		purge := *folder.PurgeAt
		copied.PurgeAt = &purge
	}
	return &copied
}
