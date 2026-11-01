/* 内存上传会话仓储：按 ID 保存会话，写入变更登记撤销动作 */
package memory

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* UploadRepository 是 port.UploadRepository 的内存实现 */
type UploadRepository struct {
	store *Store
	byID  map[string]*entity.Upload
}

/* NewUploadRepository 绑定共享存储并初始化会话集合 */
func NewUploadRepository(store *Store) *UploadRepository {
	return &UploadRepository{
		store: store,
		byID:  make(map[string]*entity.Upload),
	}
}

/* CreateBatch 批量写入会话；任一 ID 冲突即整体失败，符合 G-07 的事务语义 */
func (r *UploadRepository) CreateBatch(ctx context.Context, items []*entity.Upload) error {
	release := r.store.lock(ctx)
	defer release()

	for _, item := range items {
		if _, exists := r.byID[item.ID]; exists {
			return errs.New(errs.CodeInternal, "上传会话 ID 冲突")
		}
	}
	created := make([]string, 0, len(items))
	for _, item := range items {
		r.byID[item.ID] = cloneUpload(item)
		created = append(created, item.ID)
	}
	r.store.onRollback(ctx, func() {
		for _, id := range created {
			delete(r.byID, id)
		}
	})
	return nil
}

/* GetByID 按 ID 与属主查找；不存在或不属于该用户返回 UPLOAD_NOT_FOUND */
func (r *UploadRepository) GetByID(ctx context.Context, userID, uploadID string) (*entity.Upload, error) {
	release := r.store.lock(ctx)
	defer release()

	upload, exists := r.byID[uploadID]
	if !exists || upload.UserID != userID {
		return nil, errs.New(errs.CodeUploadNotFound, "上传会话不存在")
	}
	return cloneUpload(upload), nil
}

/* Update 覆盖会话行；不存在返回 UPLOAD_NOT_FOUND */
func (r *UploadRepository) Update(ctx context.Context, upload *entity.Upload) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[upload.ID]
	if !exists {
		return errs.New(errs.CodeUploadNotFound, "上传会话不存在")
	}
	r.byID[upload.ID] = cloneUpload(upload)
	r.store.onRollback(ctx, func() { r.byID[upload.ID] = previous })
	return nil
}

/* Consume 把 READY 会话原子置为 USED；返回 false 表示会话已被其他请求消费 */
func (r *UploadRepository) Consume(ctx context.Context, userID, uploadID string) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	upload, exists := r.byID[uploadID]
	if !exists || upload.UserID != userID {
		return false, nil
	}
	if !upload.IsReady() {
		return false, nil
	}
	previous := *upload
	upload.Status = entity.UploadStatusUsed
	r.store.onRollback(ctx, func() { stored := previous; r.byID[uploadID] = &stored })
	return true, nil
}

/* cloneUpload 深拷贝会话，避免调用方修改仓储内对象 */
func cloneUpload(upload *entity.Upload) *entity.Upload {
	if upload == nil {
		return nil
	}
	copied := *upload
	return &copied
}
