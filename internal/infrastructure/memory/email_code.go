/* 内存验证码仓储：按邮箱与用途维护发送顺序，支持批量失效 */
package memory

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* EmailCodeRepository 是 port.EmailCodeRepository 的内存实现 */
type EmailCodeRepository struct {
	store *Store
	byID  map[string]*entity.EmailCode
	order map[string][]string
}

/* NewEmailCodeRepository 初始化记录表与「邮箱 + 用途」顺序索引 */
func NewEmailCodeRepository(store *Store) *EmailCodeRepository {
	return &EmailCodeRepository{
		store: store,
		byID:  make(map[string]*entity.EmailCode),
		order: make(map[string][]string),
	}
}

/* Create 追加一条发送记录；同一邮箱最多保留有限历史，不在此清理 */
func (r *EmailCodeRepository) Create(ctx context.Context, code *entity.EmailCode) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byID[code.ID]; exists {
		return errs.New(errs.CodeInternal, "验证码 ID 冲突")
	}
	r.byID[code.ID] = cloneCode(code)
	key := codeKey(code.Email, code.Purpose)
	r.order[key] = append(r.order[key], code.ID)
	r.store.onRollback(ctx, func() {
		delete(r.byID, code.ID)
		r.removeFromOrder(key, code.ID)
	})
	return nil
}

/* LatestActive 返回最近一条未消费记录；不判断是否过期，由实体消费时判定 */
func (r *EmailCodeRepository) LatestActive(ctx context.Context, email, purpose string) (*entity.EmailCode, error) {
	release := r.store.lock(ctx)
	defer release()

	ids := r.order[codeKey(email, purpose)]
	for index := len(ids) - 1; index >= 0; index-- {
		code := r.byID[ids[index]]
		if code != nil && code.ConsumedAt == nil {
			return cloneCode(code), nil
		}
	}
	return nil, errs.New(errs.CodeResourceNotFound, "验证码不存在")
}

/* Update 覆盖记录（错误次数与消费时间） */
func (r *EmailCodeRepository) Update(ctx context.Context, code *entity.EmailCode) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[code.ID]
	if !exists {
		return errs.New(errs.CodeResourceNotFound, "验证码不存在")
	}
	r.byID[code.ID] = cloneCode(code)
	r.store.onRollback(ctx, func() { r.byID[code.ID] = previous })
	return nil
}

/*
 * InvalidateActive 把该邮箱该用途下所有未消费记录置为已消费。
 * 用途：发送新验证码后旧码必须立即失效，防止多个验证码同时有效。
 */
func (r *EmailCodeRepository) InvalidateActive(ctx context.Context, email, purpose string, now time.Time) error {
	release := r.store.lock(ctx)
	defer release()

	restores := make([]func(), 0)
	for _, id := range r.order[codeKey(email, purpose)] {
		code := r.byID[id]
		if code == nil || code.ConsumedAt != nil {
			continue
		}
		updated := cloneCode(code)
		consumed := now
		updated.ConsumedAt = &consumed
		r.byID[id] = updated
		restores = append(restores, func() { r.byID[id] = code })
	}
	r.store.onRollback(ctx, func() {
		for index := len(restores) - 1; index >= 0; index-- {
			restores[index]()
		}
	})
	return nil
}

/* removeFromOrder 从顺序索引中删除 ID，保持切片紧凑 */
func (r *EmailCodeRepository) removeFromOrder(key, id string) {
	ids := r.order[key]
	for index, existing := range ids {
		if existing == id {
			r.order[key] = append(ids[:index], ids[index+1:]...)
			return
		}
	}
}

/* codeKey 用不可见分隔符拼接索引键，避免邮箱中的字符造成歧义 */
func codeKey(email, purpose string) string {
	return email + "\x00" + purpose
}

/* cloneCode 深拷贝验证码记录，隔离调用方与仓储状态 */
func cloneCode(code *entity.EmailCode) *entity.EmailCode {
	if code == nil {
		return nil
	}
	copied := *code
	if code.ConsumedAt != nil {
		consumed := *code.ConsumedAt
		copied.ConsumedAt = &consumed
	}
	return &copied
}
