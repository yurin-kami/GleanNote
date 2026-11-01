/* 内存刷新令牌仓储：按哈希检索，支持按用户与设备批量吊销 */
package memory

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* RefreshTokenRepository 是 port.RefreshTokenRepository 的内存实现 */
type RefreshTokenRepository struct {
	store  *Store
	byHash map[string]*entity.RefreshToken
	byUser map[string][]string
}

/* NewRefreshTokenRepository 初始化哈希索引与用户维度索引 */
func NewRefreshTokenRepository(store *Store) *RefreshTokenRepository {
	return &RefreshTokenRepository{
		store:  store,
		byHash: make(map[string]*entity.RefreshToken),
		byUser: make(map[string][]string),
	}
}

/* Create 保存令牌哈希；同一哈希重复写入视为内部错误 */
func (r *RefreshTokenRepository) Create(ctx context.Context, token *entity.RefreshToken) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byHash[token.TokenHash]; exists {
		return errs.New(errs.CodeInternal, "刷新令牌哈希冲突")
	}
	r.byHash[token.TokenHash] = cloneRefresh(token)
	r.byUser[token.UserID] = append(r.byUser[token.UserID], token.TokenHash)
	r.store.onRollback(ctx, func() {
		delete(r.byHash, token.TokenHash)
		r.removeUserHash(token.UserID, token.TokenHash)
	})
	return nil
}

/* GetByHash 按摘要查找令牌；未找到返回 RESOURCE_NOT_FOUND */
func (r *RefreshTokenRepository) GetByHash(ctx context.Context, tokenHash string) (*entity.RefreshToken, error) {
	release := r.store.lock(ctx)
	defer release()

	token, exists := r.byHash[tokenHash]
	if !exists {
		return nil, errs.New(errs.CodeResourceNotFound, "刷新令牌不存在")
	}
	return cloneRefresh(token), nil
}

/*
 * GetForUpdate 内存实现与 GetByHash 等价：仓储锁已把并发写入串行化，
 * 事务内的条件更新仍按 revoked_at 判定，保证并发刷新只有一个成功。
 */
func (r *RefreshTokenRepository) GetForUpdate(ctx context.Context, tokenHash string) (*entity.RefreshToken, error) {
	return r.GetByHash(ctx, tokenHash)
}

/* Update 仅在未吊销时覆盖令牌状态；返回 false 表示已被并发轮换或吊销 */
func (r *RefreshTokenRepository) Update(ctx context.Context, token *entity.RefreshToken) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byHash[token.TokenHash]
	if !exists {
		return false, errs.New(errs.CodeResourceNotFound, "刷新令牌不存在")
	}
	if previous.RevokedAt != nil {
		return false, nil
	}
	r.byHash[token.TokenHash] = cloneRefresh(token)
	r.store.onRollback(ctx, func() { r.byHash[token.TokenHash] = previous })
	return true, nil
}

/*
 * RevokeAll 吊销用户全部未吊销令牌。
 * 用途：重置密码、注销账号与检测到令牌重放后的兜底处置。
 */
func (r *RefreshTokenRepository) RevokeAll(ctx context.Context, userID string, now time.Time) error {
	return r.revokeMatching(ctx, userID, now, func(*entity.RefreshToken) bool { return true })
}

/* RevokeDevice 只吊销指定设备令牌；设备为空时按用户维度处理 */
func (r *RefreshTokenRepository) RevokeDevice(ctx context.Context, userID, deviceID string, now time.Time) error {
	if deviceID == "" {
		return r.RevokeAll(ctx, userID, now)
	}
	return r.revokeMatching(ctx, userID, now, func(token *entity.RefreshToken) bool {
		return token.DeviceID == deviceID
	})
}

/* revokeMatching 对满足条件的令牌写入吊销时间，并登记整体撤销 */
func (r *RefreshTokenRepository) revokeMatching(ctx context.Context, userID string, now time.Time, match func(*entity.RefreshToken) bool) error {
	release := r.store.lock(ctx)
	defer release()

	restores := make([]func(), 0)
	for _, hash := range r.byUser[userID] {
		token := r.byHash[hash]
		if token == nil || token.RevokedAt != nil || !match(token) {
			continue
		}
		updated := cloneRefresh(token)
		updated.Revoke(now, "")
		r.byHash[hash] = updated
		restores = append(restores, func() { r.byHash[hash] = token })
	}
	r.store.onRollback(ctx, func() {
		for index := len(restores) - 1; index >= 0; index-- {
			restores[index]()
		}
	})
	return nil
}

/* removeUserHash 从用户索引中删除哈希，避免重复吊销遍历已删除记录 */
func (r *RefreshTokenRepository) removeUserHash(userID, hash string) {
	hashes := r.byUser[userID]
	for index, existing := range hashes {
		if existing == hash {
			r.byUser[userID] = append(hashes[:index], hashes[index+1:]...)
			return
		}
	}
}

/* cloneRefresh 深拷贝令牌记录，隔离调用方与仓储状态 */
func cloneRefresh(token *entity.RefreshToken) *entity.RefreshToken {
	if token == nil {
		return nil
	}
	copied := *token
	if token.RevokedAt != nil {
		revoked := *token.RevokedAt
		copied.RevokedAt = &revoked
	}
	return &copied
}
