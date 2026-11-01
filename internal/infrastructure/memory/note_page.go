/* 内存页面仓储：按用户与笔记分桶保存页面，支持键集分页与批量统计 */
package memory

import (
	"context"
	"sort"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* NotePageRepository 是 port.NotePageRepository 的内存实现 */
type NotePageRepository struct {
	store  *Store
	byNote map[string][]*entity.NotePage
}

/* NewNotePageRepository 初始化页面分桶；桶键为 userID+"/"+noteID */
func NewNotePageRepository(store *Store) *NotePageRepository {
	return &NotePageRepository{
		store:  store,
		byNote: make(map[string][]*entity.NotePage),
	}
}

/* CreateBatch 批量追加页面；ID 冲突整体失败，保持页面集合一致 */
func (r *NotePageRepository) CreateBatch(ctx context.Context, pages []*entity.NotePage) error {
	release := r.store.lock(ctx)
	defer release()

	seen := make(map[string]struct{}, len(pages))
	appended := make([]string, 0, len(pages))
	for _, page := range pages {
		if _, exists := seen[page.ID]; exists {
			return errs.New(errs.CodeInternal, "页面 ID 冲突")
		}
		for _, existing := range r.byNote[noteBucketKey(page.UserID, page.NoteID)] {
			if existing.ID == page.ID {
				return errs.New(errs.CodeInternal, "页面 ID 冲突")
			}
		}
		seen[page.ID] = struct{}{}
	}
	for _, page := range pages {
		key := noteBucketKey(page.UserID, page.NoteID)
		r.byNote[key] = append(r.byNote[key], cloneNotePage(page))
		appended = append(appended, key)
		r.store.recordChange(ctx, pageChangeEvent(page, page.CreatedAt))
	}
	r.store.onRollback(ctx, func() {
		for _, key := range appended {
			bucket := r.byNote[key]
			if len(bucket) > 0 {
				r.byNote[key] = bucket[:len(bucket)-1]
			}
		}
	})
	return nil
}

/* GetByID 按用户、笔记与页面校验归属；未找到返回 RESOURCE_NOT_FOUND */
func (r *NotePageRepository) GetByID(ctx context.Context, userID, noteID, pageID string) (*entity.NotePage, error) {
	release := r.store.lock(ctx)
	defer release()

	for _, page := range r.byNote[noteBucketKey(userID, noteID)] {
		if page.ID == pageID {
			return cloneNotePage(page), nil
		}
	}
	return nil, errs.New(errs.CodeResourceNotFound, "页面不存在")
}

/* List 按 Index 升序返回一页；After 为排他游标，Limit 控制本页条数 */
func (r *NotePageRepository) List(ctx context.Context, query port.NotePageQuery) ([]*entity.NotePage, error) {
	pages, err := r.ListAll(ctx, query.UserID, query.NoteID)
	if err != nil {
		return nil, err
	}
	filtered := make([]*entity.NotePage, 0, len(pages))
	for _, page := range pages {
		if query.After != nil && !afterCursor(page, *query.After) {
			continue
		}
		filtered = append(filtered, page)
	}
	if query.Limit > 0 && len(filtered) > query.Limit {
		filtered = filtered[:query.Limit]
	}
	return filtered, nil
}

/* ListAll 返回笔记全部页面，按 Index 升序；用于转换任务与重试复用 */
func (r *NotePageRepository) ListAll(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error) {
	release := r.store.lock(ctx)
	defer release()

	bucket := r.byNote[noteBucketKey(userID, noteID)]
	sorted := make([]*entity.NotePage, len(bucket))
	for index, page := range bucket {
		sorted[index] = cloneNotePage(page)
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		if sorted[left].Index != sorted[right].Index {
			return sorted[left].Index < sorted[right].Index
		}
		return sorted[left].ID < sorted[right].ID
	})
	return sorted, nil
}

/* CountByNote 统计单篇笔记的页面数，供详情回填 pageCount */
func (r *NotePageRepository) CountByNote(ctx context.Context, userID, noteID string) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	return len(r.byNote[noteBucketKey(userID, noteID)]), nil
}

/* CountByNotes 批量统计页面数，一次列表查询即可回填 pageCount，避免 N+1 */
func (r *NotePageRepository) CountByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]int, error) {
	release := r.store.lock(ctx)
	defer release()

	counts := make(map[string]int, len(noteIDs))
	for _, noteID := range noteIDs {
		counts[noteID] = len(r.byNote[noteBucketKey(userID, noteID)])
	}
	return counts, nil
}

/* FirstByNotes 返回每篇笔记 Index 最小的页面，作为列表缩略图来源 */
func (r *NotePageRepository) FirstByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]*entity.NotePage, error) {
	release := r.store.lock(ctx)
	defer release()

	first := make(map[string]*entity.NotePage, len(noteIDs))
	for _, noteID := range noteIDs {
		for _, page := range r.byNote[noteBucketKey(userID, noteID)] {
			current, exists := first[noteID]
			if !exists || page.Index < current.Index {
				first[noteID] = page
			}
		}
	}
	result := make(map[string]*entity.NotePage, len(first))
	for noteID, page := range first {
		result[noteID] = cloneNotePage(page)
	}
	return result, nil
}

/*
 * DeleteByNote 移除笔记全部页面并返回被删除行。
 * 返回值供用例释放容量与清理对象，事务回滚时恢复原分桶。
 */
func (r *NotePageRepository) DeleteByNote(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error) {
	release := r.store.lock(ctx)
	defer release()

	key := noteBucketKey(userID, noteID)
	previous, exists := r.byNote[key]
	if !exists || len(previous) == 0 {
		return nil, nil
	}
	deleted := make([]*entity.NotePage, len(previous))
	for index, page := range previous {
		deleted[index] = cloneNotePage(page)
	}
	delete(r.byNote, key)
	r.store.onRollback(ctx, func() { r.byNote[key] = previous })
	return deleted, nil
}

/* noteBucketKey 生成分桶键；UUID 不含斜杠，拼接不会产生歧义 */
func noteBucketKey(userID, noteID string) string {
	return userID + "/" + noteID
}

/* afterCursor 判断页面是否严格位于游标之后，与键集分页口径一致 */
func afterCursor(page *entity.NotePage, cursor port.NotePageCursor) bool {
	if page.Index != cursor.Index {
		return page.Index > cursor.Index
	}
	return page.ID > cursor.ID
}

/* cloneNotePage 深拷贝页面，避免调用方修改仓储内对象 */
func cloneNotePage(page *entity.NotePage) *entity.NotePage {
	if page == nil {
		return nil
	}
	copied := *page
	return &copied
}
