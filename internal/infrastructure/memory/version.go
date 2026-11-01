/* 内存版本仓储：按用户分组保存正文快照，支持按笔记倒序读取与保留期清理 */
package memory

import (
	"context"
	"sort"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* NoteVersionRepository 是 port.NoteVersionRepository 的内存实现 */
type NoteVersionRepository struct {
	store  *Store
	byUser map[string][]*entity.NoteVersion
}

/* NewNoteVersionRepository 初始化按用户分组的版本集合 */
func NewNoteVersionRepository(store *Store) *NoteVersionRepository {
	return &NoteVersionRepository{
		store:  store,
		byUser: make(map[string][]*entity.NoteVersion),
	}
}

/* Create 追加版本快照并拒绝 ID 冲突 */
func (r *NoteVersionRepository) Create(ctx context.Context, version *entity.NoteVersion) error {
	release := r.store.lock(ctx)
	defer release()

	for _, existing := range r.byUser[version.UserID] {
		if existing.ID == version.ID {
			return errs.New(errs.CodeInternal, "版本 ID 冲突")
		}
	}
	r.byUser[version.UserID] = append(r.byUser[version.UserID], cloneVersion(version))
	r.store.onRollback(ctx, func() {
		items := r.byUser[version.UserID]
		for index, existing := range items {
			if existing.ID == version.ID {
				r.byUser[version.UserID] = append(items[:index], items[index+1:]...)
				return
			}
		}
	})
	return nil
}

/* GetByID 返回指定版本；未找到或不属于该笔记返回 RESOURCE_NOT_FOUND */
func (r *NoteVersionRepository) GetByID(ctx context.Context, userID, noteID, versionID string) (*entity.NoteVersion, error) {
	release := r.store.lock(ctx)
	defer release()

	for _, version := range r.byUser[userID] {
		if version.ID == versionID && version.NoteID == noteID {
			return cloneVersion(version), nil
		}
	}
	return nil, errs.New(errs.CodeResourceNotFound, "版本不存在")
}

/* ListByNote 按生成时间倒序返回笔记的全部版本 */
func (r *NoteVersionRepository) ListByNote(ctx context.Context, userID, noteID string) ([]*entity.NoteVersion, error) {
	release := r.store.lock(ctx)
	defer release()

	items := make([]*entity.NoteVersion, 0, 8)
	for _, version := range r.byUser[userID] {
		if version.NoteID == noteID {
			items = append(items, version)
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		if !items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].CreatedAt.After(items[right].CreatedAt)
		}
		return items[left].ID > items[right].ID
	})
	return cloneVersions(items), nil
}

/* DeleteByNote 删除笔记全部版本；笔记彻底删除时与正文同事务调用 */
func (r *NoteVersionRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	release := r.store.lock(ctx)
	defer release()

	previous := r.byUser[userID]
	kept := make([]*entity.NoteVersion, 0, len(previous))
	for _, version := range previous {
		if version.NoteID != noteID {
			kept = append(kept, version)
		}
	}
	r.byUser[userID] = kept
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	return nil
}

/*
 * Prune 清理超出保留数量与保留期的版本。
 * 排序口径与 ListByNote 一致：保留最新的 keep 条，其余删除；返回删除条数。
 */
func (r *NoteVersionRepository) Prune(ctx context.Context, userID, noteID string, keep int, cutoff time.Time) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	items := make([]*entity.NoteVersion, 0, 8)
	for _, version := range r.byUser[userID] {
		if version.NoteID == noteID {
			items = append(items, version)
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		if !items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].CreatedAt.After(items[right].CreatedAt)
		}
		return items[left].ID > items[right].ID
	})

	/* 倒序后前 keep 条保留，超出数量或早于截止时间的版本删除 */
	removed := 0
	kept := make([]*entity.NoteVersion, 0, len(items))
	for index, version := range items {
		if index >= keep || version.CreatedAt.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, version)
	}
	if removed == 0 {
		return 0, nil
	}
	previous := r.byUser[userID]

	/* 写回同一用户的其他笔记版本，只替换目标笔记部分 */
	merged := make([]*entity.NoteVersion, 0, len(previous)-removed)
	for _, version := range previous {
		if version.NoteID != noteID {
			merged = append(merged, version)
		}
	}
	merged = append(merged, kept...)
	r.byUser[userID] = merged
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	return removed, nil
}

/* cloneVersions 深拷贝切片，避免调用方修改仓储内对象 */
func cloneVersions(items []*entity.NoteVersion) []*entity.NoteVersion {
	if len(items) == 0 {
		return nil
	}
	copied := make([]*entity.NoteVersion, len(items))
	for index, version := range items {
		copied[index] = cloneVersion(version)
	}
	return copied
}

/* cloneVersion 深拷贝版本，避免调用方修改仓储内对象 */
func cloneVersion(version *entity.NoteVersion) *entity.NoteVersion {
	if version == nil {
		return nil
	}
	copied := *version
	return &copied
}
