/* 内存笔记仓储：按用户分组保存笔记，支持键集分页与按目录批处理 */
package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* NoteRepository 是 port.NoteRepository 的内存实现 */
type NoteRepository struct {
	store  *Store
	byUser map[string][]*entity.Note
}

/* NewNoteRepository 初始化按用户分组的笔记集合 */
func NewNoteRepository(store *Store) *NoteRepository {
	return &NoteRepository{
		store:  store,
		byUser: make(map[string][]*entity.Note),
	}
}

/* Create 追加笔记并拒绝 ID 冲突；同一目录允许重名标题 */
func (r *NoteRepository) Create(ctx context.Context, note *entity.Note) error {
	release := r.store.lock(ctx)
	defer release()

	for _, existing := range r.byUser[note.UserID] {
		if existing.ID == note.ID {
			return errs.New(errs.CodeInternal, "笔记 ID 冲突")
		}
	}
	r.byUser[note.UserID] = append(r.byUser[note.UserID], cloneNote(note))
	r.store.onRollback(ctx, func() {
		notes := r.byUser[note.UserID]
		for index, existing := range notes {
			if existing.ID == note.ID {
				r.byUser[note.UserID] = append(notes[:index], notes[index+1:]...)
				return
			}
		}
	})
	r.store.recordChange(ctx, noteChangeEvent(note, entity.ChangeOpCreate, note.UpdatedAt))
	return nil
}

/* GetByID 返回笔记原始行（含已删除笔记）；未找到或不属于该用户返回 RESOURCE_NOT_FOUND */
func (r *NoteRepository) GetByID(ctx context.Context, userID, noteID string) (*entity.Note, error) {
	release := r.store.lock(ctx)
	defer release()

	if _, note := r.find(userID, noteID); note == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "笔记不存在")
	} else {
		return cloneNote(note), nil
	}
}

/*
 * List 返回目录内笔记；State 为空时排除回收站笔记。
 * totalSize 为过滤后的总数，Limit 控制本页条数，游标之后按升序键继续。
 */
func (r *NoteRepository) List(ctx context.Context, query port.NoteListQuery) ([]*entity.Note, int, error) {
	release := r.store.lock(ctx)
	defer release()

	candidates := make([]*entity.Note, 0)
	for _, note := range r.byUser[query.UserID] {
		if note.FolderID != query.FolderID || !matchesState(note, query.State) {
			continue
		}
		if query.UpdatedAfter != nil && !note.UpdatedAt.After(*query.UpdatedAfter) {
			continue
		}
		candidates = append(candidates, note)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := port.NoteSortKey(query.Order, candidates[left])
		rightKey := port.NoteSortKey(query.Order, candidates[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return candidates[left].ID < candidates[right].ID
	})
	total := len(candidates)
	if query.After != nil {
		candidates = filterNotesAfter(candidates, query)
	}
	if query.Limit > 0 && len(candidates) > query.Limit {
		candidates = candidates[:query.Limit]
	}
	return cloneNotes(candidates), total, nil
}

/* GetForUpdate 内存实现复用 GetByID：存储层已用全局锁串行化所有读写 */
func (r *NoteRepository) GetForUpdate(ctx context.Context, userID, noteID string) (*entity.Note, error) {
	return r.GetByID(ctx, userID, noteID)
}

/* ListByFolders 返回给定目录下的全部笔记（含回收站），供目录级联删除与彻底删除 */
func (r *NoteRepository) ListByFolders(ctx context.Context, userID string, folderIDs []string) ([]*entity.Note, error) {
	release := r.store.lock(ctx)
	defer release()

	wanted := indexOf(folderIDs)
	items := make([]*entity.Note, 0, 8)
	for _, note := range r.byUser[userID] {
		if _, ok := wanted[note.FolderID]; ok {
			items = append(items, note)
		}
	}
	return cloneNotes(items), nil
}

/* ListExpired 返回 purge_at 已到期的回收站笔记，供定时清理任务消费 */
func (r *NoteRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]*entity.Note, error) {
	release := r.store.lock(ctx)
	defer release()

	if limit <= 0 {
		limit = 100
	}
	expired := make([]*entity.Note, 0, limit)
	for _, notes := range r.byUser {
		for _, note := range notes {
			if !note.IsDeleted() || note.PurgeAt == nil || note.PurgeAt.After(now) {
				continue
			}
			expired = append(expired, note)
		}
	}
	sort.SliceStable(expired, func(left, right int) bool {
		return expired[left].PurgeAt.Before(*expired[right].PurgeAt)
	})
	if len(expired) > limit {
		expired = expired[:limit]
	}
	return cloneNotes(expired), nil
}

/* Update 覆盖笔记行；不存在返回 RESOURCE_NOT_FOUND */
func (r *NoteRepository) Update(ctx context.Context, note *entity.Note) error {
	_, err := r.write(ctx, note, -1)
	return err
}

/* UpdateWithRevision 以 revision 条件覆盖笔记行；expected < 0 表示不做乐观锁校验 */
func (r *NoteRepository) UpdateWithRevision(ctx context.Context, note *entity.Note, expected int) (bool, error) {
	return r.write(ctx, note, expected)
}

/* write 是 Update/UpdateWithRevision 的共用实现；expected 不匹配时不写任何数据 */
func (r *NoteRepository) write(ctx context.Context, note *entity.Note, expected int) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	index, found := r.find(note.UserID, note.ID)
	if found == nil {
		return false, errs.New(errs.CodeResourceNotFound, "笔记不存在")
	}
	if expected >= 0 && found.Revision != expected {
		return false, nil
	}
	previous := cloneNote(found)
	r.byUser[note.UserID][index] = cloneNote(note)
	r.store.onRollback(ctx, func() { r.byUser[note.UserID][index] = previous })
	/* 软删除转为墓碑事件，恢复与编辑为更新事件（docs/API.md#510-增量同步） */
	op := entity.ChangeOpUpdate
	if !previous.IsDeleted() && note.IsDeleted() {
		op = entity.ChangeOpDelete
	}
	r.store.recordChange(ctx, noteChangeEvent(note, op, note.UpdatedAt))
	return true, nil
}

/* Delete 物理删除单篇笔记；不存在返回 RESOURCE_NOT_FOUND */
func (r *NoteRepository) Delete(ctx context.Context, userID, noteID string) error {
	release := r.store.lock(ctx)
	defer release()

	index, found := r.find(userID, noteID)
	if found == nil {
		return errs.New(errs.CodeResourceNotFound, "笔记不存在")
	}
	previous := r.byUser[userID]
	kept := make([]*entity.Note, 0, len(previous)-1)
	kept = append(kept, previous[:index]...)
	kept = append(kept, previous[index+1:]...)
	r.byUser[userID] = kept
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	r.store.recordChange(ctx, noteChangeEvent(found, entity.ChangeOpDelete, nowFunc()))
	return nil
}

/* DeleteAllByUser 清除账号全部笔记；注销用例在一个事务内调用 */
func (r *NoteRepository) DeleteAllByUser(ctx context.Context, userID string) error {
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

/* CountActiveByFolder 统计给定目录下未删除笔记数，供目录列表回填 noteCount */
func (r *NoteRepository) CountActiveByFolder(ctx context.Context, userID string, folderIDs []string) (map[string]int, error) {
	release := r.store.lock(ctx)
	defer release()

	counts := make(map[string]int, len(folderIDs))
	wanted := make(map[string]struct{}, len(folderIDs))
	for _, id := range folderIDs {
		wanted[id] = struct{}{}
	}
	for _, note := range r.byUser[userID] {
		if note.IsDeleted() {
			continue
		}
		if _, ok := wanted[note.FolderID]; ok {
			counts[note.FolderID]++
		}
	}
	return counts, nil
}

/* MarkDeletedByFolders 把目录内未删除笔记并入同一删除批次，返回受影响数量 */
func (r *NoteRepository) MarkDeletedByFolders(ctx context.Context, userID string, folderIDs []string,
	batchID string, now time.Time, retention time.Duration) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	wanted := indexOf(folderIDs)
	affected := 0
	for _, note := range r.byUser[userID] {
		if note.IsDeleted() {
			continue
		}
		if _, ok := wanted[note.FolderID]; !ok {
			continue
		}
		previous := cloneNote(note)
		index, _ := r.find(userID, note.ID)
		note.MarkDeleted(batchID, now, retention)
		r.byUser[userID][index] = cloneNote(note)
		r.store.onRollback(ctx, func() { r.byUser[userID][index] = previous })
		r.store.recordChange(ctx, noteChangeEvent(note, entity.ChangeOpDelete, now))
		affected++
	}
	return affected, nil
}

/* RestoreByBatch 恢复同一删除批次内的笔记，目录沿用删除前的归属 */
func (r *NoteRepository) RestoreByBatch(ctx context.Context, userID, batchID string, now time.Time) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	affected := 0
	for _, note := range r.byUser[userID] {
		if !note.IsDeleted() || note.DeleteBatchID != batchID {
			continue
		}
		previous := cloneNote(note)
		index, _ := r.find(userID, note.ID)
		note.Restore(note.FolderID, now)
		r.byUser[userID][index] = cloneNote(note)
		r.store.onRollback(ctx, func() { r.byUser[userID][index] = previous })
		r.store.recordChange(ctx, noteChangeEvent(note, entity.ChangeOpUpdate, now))
		affected++
	}
	return affected, nil
}

/*
 * Search 在未删除笔记中按标题与正文做大小写不敏感的子串匹配。
 * 排序：标题命中优先，同级按更新时间倒序；totalSize 为全部命中数，Limit 控制本页。
 * 参考：docs/API.md#5311-get-v1notessearch、PRD FR-12
 */
func (r *NoteRepository) Search(ctx context.Context, query port.NoteSearchQuery) ([]*entity.Note, int, error) {
	release := r.store.lock(ctx)
	defer release()

	folderSet := setOfStrings(query.FolderIDs)
	noteSet := setOfStrings(query.NoteIDs)
	candidates := make([]*entity.Note, 0)
	for _, note := range r.byUser[query.UserID] {
		if note.IsDeleted() {
			continue
		}
		if folderSet != nil {
			if _, ok := folderSet[note.FolderID]; !ok {
				continue
			}
		}
		if noteSet != nil {
			if _, ok := noteSet[note.ID]; !ok {
				continue
			}
		}
		if !containsFold(note.Title, query.Keyword) && !containsFold(note.Content, query.Keyword) {
			continue
		}
		candidates = append(candidates, note)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := port.NoteSearchSortKey(containsFold(candidates[left].Title, query.Keyword), candidates[left].UpdatedAt)
		rightKey := port.NoteSearchSortKey(containsFold(candidates[right].Title, query.Keyword), candidates[right].UpdatedAt)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return candidates[left].ID < candidates[right].ID
	})
	total := len(candidates)
	if query.After != nil {
		filtered := make([]*entity.Note, 0, len(candidates))
		for _, note := range candidates {
			key := port.NoteSearchSortKey(containsFold(note.Title, query.Keyword), note.UpdatedAt)
			if key > query.After.SortKey || (key == query.After.SortKey && note.ID > query.After.ID) {
				filtered = append(filtered, note)
			}
		}
		candidates = filtered
	}
	if query.Limit > 0 && len(candidates) > query.Limit {
		candidates = candidates[:query.Limit]
	}
	return cloneNotes(candidates), total, nil
}

/* ListDeleted 返回用户全部回收站笔记，供回收站聚合按批次分组 */
func (r *NoteRepository) ListDeleted(ctx context.Context, userID string) ([]*entity.Note, error) {
	release := r.store.lock(ctx)
	defer release()

	deleted := make([]*entity.Note, 0, 8)
	for _, note := range r.byUser[userID] {
		if note.IsDeleted() {
			deleted = append(deleted, note)
		}
	}
	sort.SliceStable(deleted, func(left, right int) bool {
		if !sameTime(deleted[left].DeletedAt, deleted[right].DeletedAt) {
			return deleted[left].DeletedAt.After(*deleted[right].DeletedAt)
		}
		return deleted[left].ID < deleted[right].ID
	})
	return cloneNotes(deleted), nil
}

/* setOfStrings 把 ID 列表转为集合；nil 输入保持 nil，表示不过滤 */
func setOfStrings(ids []string) map[string]struct{} {
	if ids == nil {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

/* containsFold 判断文本是否包含关键词，忽略大小写 */
func containsFold(text, keyword string) bool {
	if keyword == "" {
		return true
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(keyword))
}

/* sameTime 比较两个删除时间指针是否都为空或等值 */
func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

/* matchesState 按查询状态过滤；未指定状态时排除回收站笔记 */
func matchesState(note *entity.Note, state *entity.NoteState) bool {
	if state == nil {
		return !note.IsDeleted()
	}
	return note.State == *state
}

/* find 返回笔记在切片中的下标与指针；不存在时 found 为 nil */
func (r *NoteRepository) find(userID, noteID string) (int, *entity.Note) {
	for index, note := range r.byUser[userID] {
		if note.ID == noteID {
			return index, note
		}
	}
	return -1, nil
}

/* indexOf 把目录 ID 列表转为集合，便于批量判断归属 */
func indexOf(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

/* filterNotesAfter 保留严格位于游标之后的笔记 */
func filterNotesAfter(items []*entity.Note, query port.NoteListQuery) []*entity.Note {
	cursor := query.After
	filtered := make([]*entity.Note, 0, len(items))
	for _, note := range items {
		key := port.NoteSortKey(query.Order, note)
		if key > cursor.SortKey || (key == cursor.SortKey && note.ID > cursor.ID) {
			filtered = append(filtered, note)
		}
	}
	return filtered
}

/* cloneNotes 深拷贝切片，避免调用方修改仓储内对象 */
func cloneNotes(notes []*entity.Note) []*entity.Note {
	if len(notes) == 0 {
		return nil
	}
	copied := make([]*entity.Note, len(notes))
	for index, note := range notes {
		copied[index] = cloneNote(note)
	}
	return copied
}

/* cloneNote 深拷贝笔记，避免调用方修改仓储内对象 */
func cloneNote(note *entity.Note) *entity.Note {
	if note == nil {
		return nil
	}
	copied := *note
	if note.DeletedAt != nil {
		deleted := *note.DeletedAt
		copied.DeletedAt = &deleted
	}
	if note.PurgeAt != nil {
		purge := *note.PurgeAt
		copied.PurgeAt = &purge
	}
	return &copied
}
