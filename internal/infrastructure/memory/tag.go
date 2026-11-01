/* 内存标签仓储：账号级标签本体与笔记-标签多对多关联 */
package memory

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TagRepository 是 port.TagRepository 的内存实现 */
type TagRepository struct {
	store  *Store
	byUser map[string][]*entity.Tag
}

/* NewTagRepository 初始化按用户分组的标签集合 */
func NewTagRepository(store *Store) *TagRepository {
	return &TagRepository{
		store:  store,
		byUser: make(map[string][]*entity.Tag),
	}
}

/* Create 追加标签并拒绝账号内重名（大小写不敏感） */
func (r *TagRepository) Create(ctx context.Context, tag *entity.Tag) error {
	release := r.store.lock(ctx)
	defer release()

	for _, existing := range r.byUser[tag.UserID] {
		if existing.ID == tag.ID {
			return errs.New(errs.CodeInternal, "标签 ID 冲突")
		}
		if strings.EqualFold(existing.Name, tag.Name) {
			return errs.New(errs.CodeNameDuplicated, "账号内已存在同名标签")
		}
	}
	r.byUser[tag.UserID] = append(r.byUser[tag.UserID], cloneTag(tag))
	r.store.onRollback(ctx, func() {
		tags := r.byUser[tag.UserID]
		for index, existing := range tags {
			if existing.ID == tag.ID {
				r.byUser[tag.UserID] = append(tags[:index], tags[index+1:]...)
				return
			}
		}
	})
	r.store.recordChange(ctx, tagChangeEvent(tag, entity.ChangeOpCreate, tag.UpdatedAt))
	return nil
}

/* GetByID 返回标签；未找到或不属于该用户返回 RESOURCE_NOT_FOUND */
func (r *TagRepository) GetByID(ctx context.Context, userID, tagID string) (*entity.Tag, error) {
	release := r.store.lock(ctx)
	defer release()

	tag := r.tag(userID, tagID)
	if tag == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "标签不存在")
	}
	return cloneTag(tag), nil
}

/* List 返回账号全部标签，排序由用例按笔记数与名称完成 */
func (r *TagRepository) List(ctx context.Context, userID string) ([]*entity.Tag, error) {
	release := r.store.lock(ctx)
	defer release()

	return cloneTags(r.byUser[userID]), nil
}

/* Update 覆盖标签行；不存在返回 RESOURCE_NOT_FOUND */
func (r *TagRepository) Update(ctx context.Context, tag *entity.Tag) error {
	release := r.store.lock(ctx)
	defer release()

	index := r.tagIndex(tag.UserID, tag.ID)
	if index < 0 {
		return errs.New(errs.CodeResourceNotFound, "标签不存在")
	}
	previous := cloneTag(r.byUser[tag.UserID][index])
	r.byUser[tag.UserID][index] = cloneTag(tag)
	r.store.onRollback(ctx, func() { r.byUser[tag.UserID][index] = previous })
	r.store.recordChange(ctx, tagChangeEvent(tag, entity.ChangeOpUpdate, tag.UpdatedAt))
	return nil
}

/* Delete 物理删除标签；不存在返回 RESOURCE_NOT_FOUND */
func (r *TagRepository) Delete(ctx context.Context, userID, tagID string) error {
	release := r.store.lock(ctx)
	defer release()

	index := r.tagIndex(userID, tagID)
	if index < 0 {
		return errs.New(errs.CodeResourceNotFound, "标签不存在")
	}
	removed := r.byUser[userID][index]
	previous := r.byUser[userID]
	kept := make([]*entity.Tag, 0, len(previous)-1)
	kept = append(kept, previous[:index]...)
	kept = append(kept, previous[index+1:]...)
	r.byUser[userID] = kept
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	r.store.recordChange(ctx, tagChangeEvent(removed, entity.ChangeOpDelete, nowFunc()))
	return nil
}

/* DeleteAllByUser 清除账号全部标签；注销用例在一个事务内调用 */
func (r *TagRepository) DeleteAllByUser(ctx context.Context, userID string) error {
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

/* tagIndex 返回标签在切片中的下标；不存在时返回 -1（调用方需持锁） */
func (r *TagRepository) tagIndex(userID, tagID string) int {
	for index, tag := range r.byUser[userID] {
		if tag.ID == tagID {
			return index
		}
	}
	return -1
}

/* tag 直接返回标签指针，供关联仓储在同一把锁内读取（不额外加锁） */
func (r *TagRepository) tag(userID, tagID string) *entity.Tag {
	index := r.tagIndex(userID, tagID)
	if index < 0 {
		return nil
	}
	return r.byUser[userID][index]
}

/*
 * NoteTagRepository 是 port.NoteTagRepository 的内存实现。
 * 关联只保存 ID，读取时回查标签本体，重命名无需级联更新。
 */
type NoteTagRepository struct {
	store  *Store
	tags   *TagRepository
	notes  *NoteRepository
	byUser map[string]map[string][]string
}

/* NewNoteTagRepository 初始化关联集合；notes 用于统计时排除回收站笔记 */
func NewNoteTagRepository(store *Store, tags *TagRepository, notes *NoteRepository) *NoteTagRepository {
	return &NoteTagRepository{
		store:  store,
		tags:   tags,
		notes:  notes,
		byUser: make(map[string]map[string][]string),
	}
}

/* ReplaceForNote 整体覆盖一篇笔记的标签关联 */
func (r *NoteTagRepository) ReplaceForNote(ctx context.Context, userID, noteID string, tagIDs []string) error {
	release := r.store.lock(ctx)
	defer release()

	links := r.linksOf(userID)
	previous, existed := links[noteID]
	links[noteID] = append([]string(nil), tagIDs...)
	r.store.onRollback(ctx, func() {
		if existed {
			links[noteID] = previous
		} else {
			delete(links, noteID)
		}
	})
	r.store.recordChange(ctx, noteTagsChangeEvent(userID, noteID, tagIDs, nowFunc()))
	return nil
}

/* TagsByNotes 批量返回每篇笔记的标签，顺序沿用设置时的顺序 */
func (r *NoteTagRepository) TagsByNotes(ctx context.Context, userID string, noteIDs []string) (map[string][]*entity.Tag, error) {
	release := r.store.lock(ctx)
	defer release()

	links := r.byUser[userID]
	result := make(map[string][]*entity.Tag, len(noteIDs))
	for _, noteID := range noteIDs {
		tagIDs := links[noteID]
		tags := make([]*entity.Tag, 0, len(tagIDs))
		for _, tagID := range tagIDs {
			if tag := r.tags.tag(userID, tagID); tag != nil {
				tags = append(tags, cloneTag(tag))
			}
		}
		result[noteID] = tags
	}
	return result, nil
}

/* NoteIDsByTags 返回同时包含全部给定标签的未删除笔记 */
func (r *NoteTagRepository) NoteIDsByTags(ctx context.Context, userID string, tagIDs []string) ([]string, error) {
	release := r.store.lock(ctx)
	defer release()

	result := make([]string, 0, 8)
	for noteID, current := range r.byUser[userID] {
		if !r.activeNote(userID, noteID) {
			continue
		}
		if containsAll(current, tagIDs) {
			result = append(result, noteID)
		}
	}
	return result, nil
}

/* CountsByTags 统计每个标签关联的未删除笔记数；tagIDs 为空表示全部标签 */
func (r *NoteTagRepository) CountsByTags(ctx context.Context, userID string, tagIDs []string) (map[string]int, error) {
	release := r.store.lock(ctx)
	defer release()

	wanted := make(map[string]struct{}, len(tagIDs))
	for _, tagID := range tagIDs {
		wanted[tagID] = struct{}{}
	}
	counts := make(map[string]int, len(wanted))
	for noteID, current := range r.byUser[userID] {
		if !r.activeNote(userID, noteID) {
			continue
		}
		for _, tagID := range current {
			if len(wanted) > 0 {
				if _, ok := wanted[tagID]; !ok {
					continue
				}
			}
			counts[tagID]++
		}
	}
	return counts, nil
}

/* DeleteByTag 解除标签的全部关联，不删除笔记 */
func (r *NoteTagRepository) DeleteByTag(ctx context.Context, userID, tagID string) error {
	release := r.store.lock(ctx)
	defer release()

	links := r.byUser[userID]
	if len(links) == 0 {
		return nil
	}
	previous := cloneLinks(links)
	for noteID, current := range links {
		kept := current[:0]
		for _, existing := range current {
			if existing != tagID {
				kept = append(kept, existing)
			}
		}
		if len(kept) == 0 {
			delete(links, noteID)
			continue
		}
		links[noteID] = kept
	}
	r.store.onRollback(ctx, func() { r.byUser[userID] = previous })
	return nil
}

/* DeleteByNote 清除单篇笔记的全部标签关联，供彻底删除使用 */
func (r *NoteTagRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	release := r.store.lock(ctx)
	defer release()

	links := r.byUser[userID]
	previous, existed := links[noteID]
	if !existed {
		return nil
	}
	delete(links, noteID)
	r.store.onRollback(ctx, func() { links[noteID] = previous })
	return nil
}

/* DeleteAllByUser 清除账号全部关联；注销用例在一个事务内调用 */
func (r *NoteTagRepository) DeleteAllByUser(ctx context.Context, userID string) error {
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

/* linksOf 返回用户级关联表，不存在时惰性创建 */
func (r *NoteTagRepository) linksOf(userID string) map[string][]string {
	links, exists := r.byUser[userID]
	if !exists {
		links = make(map[string][]string)
		r.byUser[userID] = links
	}
	return links
}

/* activeNote 判断笔记是否存在且未进入回收站；调用方需持锁 */
func (r *NoteTagRepository) activeNote(userID, noteID string) bool {
	_, note := r.notes.find(userID, noteID)
	return note != nil && !note.IsDeleted()
}

/* containsAll 判断 current 是否包含全部 wanted */
func containsAll(current, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(current))
	for _, id := range current {
		set[id] = struct{}{}
	}
	for _, id := range wanted {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

/* cloneLinks 深拷贝关联表，用于删除操作的撤销 */
func cloneLinks(links map[string][]string) map[string][]string {
	copied := make(map[string][]string, len(links))
	for noteID, tagIDs := range links {
		copied[noteID] = append([]string(nil), tagIDs...)
	}
	return copied
}

/* cloneTag 深拷贝标签，避免调用方修改仓储内对象 */
func cloneTag(tag *entity.Tag) *entity.Tag {
	if tag == nil {
		return nil
	}
	copied := *tag
	return &copied
}

/* cloneTags 深拷贝切片，避免调用方修改仓储内对象 */
func cloneTags(tags []*entity.Tag) []*entity.Tag {
	if len(tags) == 0 {
		return nil
	}
	copied := make([]*entity.Tag, len(tags))
	for index, tag := range tags {
		copied[index] = cloneTag(tag)
	}
	return copied
}
