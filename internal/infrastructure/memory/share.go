/* 内存分享仓储：按用户与笔记保存分享状态，并维护令牌索引 */
package memory

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* tokenRef 指向令牌对应的笔记，供匿名公开读取定位 */
type tokenRef struct {
	userID string
	noteID string
}

/* ShareLinkRepository 是 port.ShareLinkRepository 的内存实现 */
type ShareLinkRepository struct {
	store   *Store
	byUser  map[string]map[string]*entity.ShareLink
	byToken map[string]tokenRef
}

/* NewShareLinkRepository 初始化分享状态与令牌索引 */
func NewShareLinkRepository(store *Store) *ShareLinkRepository {
	return &ShareLinkRepository{
		store:   store,
		byUser:  make(map[string]map[string]*entity.ShareLink),
		byToken: make(map[string]tokenRef),
	}
}

/* GetByNote 返回笔记的分享状态；从未开启过分享时返回 RESOURCE_NOT_FOUND */
func (r *ShareLinkRepository) GetByNote(ctx context.Context, userID, noteID string) (*entity.ShareLink, error) {
	release := r.store.lock(ctx)
	defer release()

	link, ok := r.byUser[userID][noteID]
	if !ok || link == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "分享链接不存在")
	}
	return cloneShareLink(link), nil
}

/* GetByToken 按令牌索引返回分享状态；令牌不存在返回 RESOURCE_NOT_FOUND */
func (r *ShareLinkRepository) GetByToken(ctx context.Context, token string) (*entity.ShareLink, error) {
	release := r.store.lock(ctx)
	defer release()

	ref, ok := r.byToken[token]
	if !ok {
		return nil, errs.New(errs.CodeResourceNotFound, "分享链接不存在")
	}
	link := r.byUser[ref.userID][ref.noteID]
	if link == nil {
		return nil, errs.New(errs.CodeResourceNotFound, "分享链接不存在")
	}
	return cloneShareLink(link), nil
}

/* Save 插入或覆盖分享状态，并同步令牌索引 */
func (r *ShareLinkRepository) Save(ctx context.Context, link *entity.ShareLink) error {
	release := r.store.lock(ctx)
	defer release()

	if r.byUser[link.UserID] == nil {
		r.byUser[link.UserID] = make(map[string]*entity.ShareLink)
	}
	previous, existed := r.byUser[link.UserID][link.NoteID]
	previousTokenKey := previousToken(previous)
	previousRef, hadRef := r.byToken[previousTokenKey]

	r.byUser[link.UserID][link.NoteID] = cloneShareLink(link)
	if hadRef {
		delete(r.byToken, previousTokenKey)
	}
	if link.Enabled && link.Token != "" {
		r.byToken[link.Token] = tokenRef{userID: link.UserID, noteID: link.NoteID}
	}

	r.store.onRollback(ctx, func() {
		if existed {
			r.byUser[link.UserID][link.NoteID] = previous
		} else {
			delete(r.byUser[link.UserID], link.NoteID)
		}
		delete(r.byToken, link.Token)
		if hadRef {
			r.byToken[previousTokenKey] = previousRef
		}
	})
	return nil
}

/* Delete 移除分享状态与令牌；不存在时保持幂等成功 */
func (r *ShareLinkRepository) Delete(ctx context.Context, userID, noteID string) error {
	release := r.store.lock(ctx)
	defer release()

	previous, existed := r.byUser[userID][noteID]
	if !existed || previous == nil {
		return nil
	}
	delete(r.byUser[userID], noteID)
	delete(r.byToken, previous.Token)

	r.store.onRollback(ctx, func() {
		r.byUser[userID][noteID] = previous
		if previous.Token != "" {
			r.byToken[previous.Token] = tokenRef{userID: userID, noteID: noteID}
		}
	})
	return nil
}

/* DeleteByNote 兼容笔记模块的最小接口命名，语义与 Delete 一致 */
func (r *ShareLinkRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	return r.Delete(ctx, userID, noteID)
}

/* EnabledByNotes 批量返回笔记分享是否开启，供笔记详情回填 shareEnabled */
func (r *ShareLinkRepository) EnabledByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]bool, error) {
	release := r.store.lock(ctx)
	defer release()

	enabled := make(map[string]bool, len(noteIDs))
	for _, noteID := range noteIDs {
		link := r.byUser[userID][noteID]
		if link != nil && link.Enabled {
			enabled[noteID] = true
		}
	}
	return enabled, nil
}

/* previousToken 返回旧记录的令牌；无记录时为空串 */
func previousToken(link *entity.ShareLink) string {
	if link == nil {
		return ""
	}
	return link.Token
}

/* cloneShareLink 深拷贝分享状态，避免调用方修改仓储内对象 */
func cloneShareLink(link *entity.ShareLink) *entity.ShareLink {
	if link == nil {
		return nil
	}
	copied := *link
	return &copied
}
