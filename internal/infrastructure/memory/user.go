/* 内存用户仓储：以 ID 与邮箱双索引保存账号，写入变更登记撤销动作 */
package memory

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* UserRepository 是 port.UserRepository 的内存实现 */
type UserRepository struct {
	store   *Store
	byID    map[string]*entity.User
	byEmail map[string]string
}

/* NewUserRepository 绑定共享存储并初始化双索引 */
func NewUserRepository(store *Store) *UserRepository {
	return &UserRepository{
		store:   store,
		byID:    make(map[string]*entity.User),
		byEmail: make(map[string]string),
	}
}

/* Create 写入新账号；邮箱重复映射为 AUTH_EMAIL_ALREADY_EXISTS */
func (r *UserRepository) Create(ctx context.Context, user *entity.User) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byEmail[user.Email]; exists {
		return errs.New(errs.CodeAuthEmailAlreadyExists, "邮箱已注册")
	}
	if _, exists := r.byID[user.ID]; exists {
		return errs.New(errs.CodeInternal, "用户 ID 冲突")
	}
	r.byID[user.ID] = cloneUser(user)
	r.byEmail[user.Email] = user.ID
	r.store.onRollback(ctx, func() {
		delete(r.byID, user.ID)
		delete(r.byEmail, user.Email)
	})
	return nil
}

/* GetByEmail 按归一化邮箱查找；未找到返回 RESOURCE_NOT_FOUND */
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*entity.User, error) {
	release := r.store.lock(ctx)
	defer release()

	id, exists := r.byEmail[email]
	if !exists {
		return nil, errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	return cloneUser(r.byID[id]), nil
}

/* GetByID 按 ID 查找；未找到返回 RESOURCE_NOT_FOUND */
func (r *UserRepository) GetByID(ctx context.Context, userID string) (*entity.User, error) {
	release := r.store.lock(ctx)
	defer release()

	user, exists := r.byID[userID]
	if !exists {
		return nil, errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	return cloneUser(user), nil
}

/* Update 覆盖账号行；邮箱索引不允许通过更新改变，保持账号唯一性 */
func (r *UserRepository) Update(ctx context.Context, user *entity.User) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[user.ID]
	if !exists {
		return errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	r.byID[user.ID] = cloneUser(user)
	r.store.onRollback(ctx, func() { r.byID[user.ID] = previous })
	return nil
}

/* Delete 物理删除账号；注销要求不留状态位，故连同索引一起移除 */
func (r *UserRepository) Delete(ctx context.Context, userID string) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[userID]
	if !exists {
		return errs.New(errs.CodeResourceNotFound, "用户不存在")
	}
	delete(r.byID, userID)
	delete(r.byEmail, previous.Email)
	r.store.onRollback(ctx, func() {
		r.byID[userID] = previous
		r.byEmail[previous.Email] = userID
	})
	return nil
}

/* cloneUser 深拷贝账号，防止调用方修改仓储内对象或反向污染 */
func cloneUser(user *entity.User) *entity.User {
	if user == nil {
		return nil
	}
	copied := *user
	if user.EmailVerifiedAt != nil {
		verified := *user.EmailVerifiedAt
		copied.EmailVerifiedAt = &verified
	}
	if user.DeletedAt != nil {
		deleted := *user.DeletedAt
		copied.DeletedAt = &deleted
	}
	return &copied
}
