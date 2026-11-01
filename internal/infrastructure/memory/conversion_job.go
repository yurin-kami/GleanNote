/* 内存转换任务仓储：按 ID 保存任务，并记录每篇笔记最近一次任务 */
package memory

import (
	"context"
	"sort"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* ConversionJobRepository 是 port.ConversionJobRepository 的内存实现 */
type ConversionJobRepository struct {
	store        *Store
	byID         map[string]*entity.ConversionJob
	latestByNote map[string]string
}

/* NewConversionJobRepository 绑定共享存储并初始化任务集合 */
func NewConversionJobRepository(store *Store) *ConversionJobRepository {
	return &ConversionJobRepository{
		store:        store,
		byID:         make(map[string]*entity.ConversionJob),
		latestByNote: make(map[string]string),
	}
}

/* Create 写入新任务并把它登记为笔记最近一次任务 */
func (r *ConversionJobRepository) Create(ctx context.Context, job *entity.ConversionJob) error {
	release := r.store.lock(ctx)
	defer release()

	if _, exists := r.byID[job.ID]; exists {
		return errs.New(errs.CodeInternal, "转换任务 ID 冲突")
	}
	key := noteBucketKey(job.UserID, job.NoteID)
	previous, hadPrevious := r.latestByNote[key]
	r.byID[job.ID] = cloneConversionJob(job)
	r.latestByNote[key] = job.ID
	r.store.onRollback(ctx, func() {
		delete(r.byID, job.ID)
		if hadPrevious {
			r.latestByNote[key] = previous
		} else {
			delete(r.latestByNote, key)
		}
	})
	return nil
}

/* GetLatestByNote 返回笔记最近一次任务；从未提交过返回 CONVERSION_NOT_FOUND */
func (r *ConversionJobRepository) GetLatestByNote(ctx context.Context, userID, noteID string) (*entity.ConversionJob, error) {
	release := r.store.lock(ctx)
	defer release()

	jobID, exists := r.latestByNote[noteBucketKey(userID, noteID)]
	if !exists {
		return nil, errs.New(errs.CodeConversionNotFound, "该笔记没有转换任务")
	}
	job, exists := r.byID[jobID]
	if !exists {
		return nil, errs.New(errs.CodeConversionNotFound, "该笔记没有转换任务")
	}
	return cloneConversionJob(job), nil
}

/* GetByID 按属主查询任务；跨账号访问与不存在统一返回 CONVERSION_NOT_FOUND */
func (r *ConversionJobRepository) GetByID(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error) {
	release := r.store.lock(ctx)
	defer release()

	job, exists := r.byID[jobID]
	if !exists || job.UserID != userID {
		return nil, errs.New(errs.CodeConversionNotFound, "转换任务不存在")
	}
	return cloneConversionJob(job), nil
}

/* GetForUpdate 与 GetByID 等价：内存实现以存储锁串行化状态迁移 */
func (r *ConversionJobRepository) GetForUpdate(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error) {
	return r.GetByID(ctx, userID, jobID)
}

/* Update 是状态机 CAS：库中状态不等于 expected 时放弃写入并返回 false */
func (r *ConversionJobRepository) Update(ctx context.Context, job *entity.ConversionJob,
	expected entity.JobState) (bool, error) {
	release := r.store.lock(ctx)
	defer release()

	previous, exists := r.byID[job.ID]
	if !exists {
		return false, errs.New(errs.CodeConversionNotFound, "转换任务不存在")
	}
	if previous.State != expected {
		return false, nil
	}
	r.byID[job.ID] = cloneConversionJob(job)
	r.store.onRollback(ctx, func() { r.byID[job.ID] = previous })
	return true, nil
}

/* DeleteByNote 移除笔记全部任务，彻底删除时调用；返回删除数量供测试与日志 */
func (r *ConversionJobRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	release := r.store.lock(ctx)
	defer release()

	key := noteBucketKey(userID, noteID)
	removed := make(map[string]*entity.ConversionJob)
	for jobID, job := range r.byID {
		if job.UserID == userID && job.NoteID == noteID {
			removed[jobID] = job
			delete(r.byID, jobID)
		}
	}
	previousLatest, hadLatest := r.latestByNote[key]
	if !hadLatest && len(removed) == 0 {
		return nil
	}
	delete(r.latestByNote, key)
	r.store.onRollback(ctx, func() {
		for jobID, job := range removed {
			r.byID[jobID] = job
		}
		if hadLatest {
			r.latestByNote[key] = previousLatest
		}
	})
	return nil
}

/* ListActiveByUser 返回用户仍处于排队或处理中的任务；用于额度接口的 conversionRunning */
func (r *ConversionJobRepository) ListActiveByUser(ctx context.Context, userID string) ([]*entity.ConversionJob, error) {
	release := r.store.lock(ctx)
	defer release()

	active := make([]*entity.ConversionJob, 0)
	for _, job := range r.byID {
		if job.UserID != userID || job.IsTerminal() {
			continue
		}
		active = append(active, cloneConversionJob(job))
	}
	return active, nil
}

/* ListPending 返回更新时间早于 before 的排队任务，按创建时间升序供 Worker 补偿入队 */
func (r *ConversionJobRepository) ListPending(ctx context.Context, before time.Time,
	limit int) ([]*entity.ConversionJob, error) {
	release := r.store.lock(ctx)
	defer release()

	pending := make([]*entity.ConversionJob, 0)
	for _, job := range r.byID {
		if job.State != entity.JobStateQueued || job.UpdatedAt.After(before) {
			continue
		}
		pending = append(pending, cloneConversionJob(job))
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

/* ReleaseRunning 把上次进程中断遗留的处理中任务回落为排队，返回受影响数量 */
func (r *ConversionJobRepository) ReleaseRunning(ctx context.Context, now time.Time) (int, error) {
	release := r.store.lock(ctx)
	defer release()

	released := 0
	for id, job := range r.byID {
		if job.State != entity.JobStateRunning {
			continue
		}
		reverted := cloneConversionJob(job)
		reverted.State = entity.JobStateQueued
		reverted.Phase = entity.JobPhaseQueued
		reverted.StartedAt = nil
		reverted.UpdatedAt = now
		r.byID[id] = reverted
		released++
		r.store.onRollback(ctx, func() { r.byID[id] = job })
	}
	return released, nil
}

/* cloneConversionJob 深拷贝任务与可空时间字段，避免调用方修改仓储内对象 */
func cloneConversionJob(job *entity.ConversionJob) *entity.ConversionJob {
	if job == nil {
		return nil
	}
	copied := *job
	if job.StartedAt != nil {
		started := *job.StartedAt
		copied.StartedAt = &started
	}
	if job.FinishedAt != nil {
		finished := *job.FinishedAt
		copied.FinishedAt = &finished
	}
	return &copied
}
