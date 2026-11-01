/* 内存导出与导入任务仓储：按任务 ID 保存状态机与逐项结果 */
package memory

import (
	"context"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* ExportJobRepository 是 port.ExportJobRepository 的内存实现 */
type ExportJobRepository struct {
	store *Store
	byID  map[string]*entity.ExportJob
}

/* NewExportJobRepository 初始化空的导出任务集合 */
func NewExportJobRepository(store *Store) *ExportJobRepository {
	return &ExportJobRepository{store: store, byID: make(map[string]*entity.ExportJob)}
}

/* Create 保存新任务；ID 冲突说明生成器异常，按内部错误处理 */
func (r *ExportJobRepository) Create(ctx context.Context, job *entity.ExportJob) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byID[job.ID]; exists {
		return errs.New(errs.CodeInternal, "导出任务 ID 冲突")
	}
	r.byID[job.ID] = cloneExportJob(job)
	r.store.onRollback(ctx, func() { delete(r.byID, job.ID) })
	return nil
}

/* GetByID 返回任务副本；未找到或不属于该用户返回 RESOURCE_NOT_FOUND（G-08） */
func (r *ExportJobRepository) GetByID(ctx context.Context, userID, exportID string) (*entity.ExportJob, error) {
	release := r.store.lock(ctx)
	defer release()

	job, exists := r.byID[exportID]
	if !exists || job.UserID != userID {
		return nil, errs.New(errs.CodeResourceNotFound, "导出任务不存在")
	}
	return cloneExportJob(job), nil
}

/* Update 覆盖任务行；任务不存在返回 RESOURCE_NOT_FOUND */
func (r *ExportJobRepository) Update(ctx context.Context, job *entity.ExportJob) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[job.ID]
	if !exists || previous.UserID != job.UserID {
		return errs.New(errs.CodeResourceNotFound, "导出任务不存在")
	}
	r.byID[job.ID] = cloneExportJob(job)
	r.store.onRollback(ctx, func() { r.byID[job.ID] = previous })
	return nil
}

/* ImportJobRepository 是 port.ImportJobRepository 的内存实现 */
type ImportJobRepository struct {
	store *Store
	byID  map[string]*entity.ImportJob
}

/* NewImportJobRepository 初始化空的导入任务集合 */
func NewImportJobRepository(store *Store) *ImportJobRepository {
	return &ImportJobRepository{store: store, byID: make(map[string]*entity.ImportJob)}
}

/* Create 保存新任务；ID 冲突说明生成器异常，按内部错误处理 */
func (r *ImportJobRepository) Create(ctx context.Context, job *entity.ImportJob) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byID[job.ID]; exists {
		return errs.New(errs.CodeInternal, "导入任务 ID 冲突")
	}
	r.byID[job.ID] = cloneImportJob(job)
	r.store.onRollback(ctx, func() { delete(r.byID, job.ID) })
	return nil
}

/* GetByID 返回任务副本；未找到或不属于该用户返回 RESOURCE_NOT_FOUND（G-08） */
func (r *ImportJobRepository) GetByID(ctx context.Context, userID, importID string) (*entity.ImportJob, error) {
	release := r.store.lock(ctx)
	defer release()

	job, exists := r.byID[importID]
	if !exists || job.UserID != userID {
		return nil, errs.New(errs.CodeResourceNotFound, "导入任务不存在")
	}
	return cloneImportJob(job), nil
}

/* Update 覆盖任务行；任务不存在返回 RESOURCE_NOT_FOUND */
func (r *ImportJobRepository) Update(ctx context.Context, job *entity.ImportJob) error {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[job.ID]
	if !exists || previous.UserID != job.UserID {
		return errs.New(errs.CodeResourceNotFound, "导入任务不存在")
	}
	r.byID[job.ID] = cloneImportJob(job)
	r.store.onRollback(ctx, func() { r.byID[job.ID] = previous })
	return nil
}

/* cloneExportJob 深拷贝导出任务，含下载有效期指针 */
func cloneExportJob(job *entity.ExportJob) *entity.ExportJob {
	if job == nil {
		return nil
	}
	copied := *job
	if job.ExpiresAt != nil {
		expiresAt := *job.ExpiresAt
		copied.ExpiresAt = &expiresAt
	}
	if job.FinishedAt != nil {
		finishedAt := *job.FinishedAt
		copied.FinishedAt = &finishedAt
	}
	return &copied
}

/* cloneImportJob 深拷贝导入任务与逐项记录 */
func cloneImportJob(job *entity.ImportJob) *entity.ImportJob {
	if job == nil {
		return nil
	}
	copied := *job
	copied.Items = make([]*entity.ImportItem, 0, len(job.Items))
	for _, item := range job.Items {
		itemCopy := *item
		copied.Items = append(copied.Items, &itemCopy)
	}
	if job.FinishedAt != nil {
		finishedAt := *job.FinishedAt
		copied.FinishedAt = &finishedAt
	}
	return &copied
}

/* 确保实现端口契约 */
var (
	_ port.ExportJobRepository = (*ExportJobRepository)(nil)
	_ port.ImportJobRepository = (*ImportJobRepository)(nil)
)
