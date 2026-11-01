/*
 * 上传、页面、转换任务、额度与容量的持久化端口。
 *
 * 职责：以领域实体为边界描述写入与查询，隐藏 SQL、索引与并发实现。
 * 不负责：状态迁移与业务校验（实体与用例负责）；按 ID 查询仍须携带 userID（G-08）。
 * 依赖：domain/entity。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/ARCHITECTURE.md#88-额度与容量
 */
package port

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/*
 * UploadRepository 管理上传会话；批量创建保证一次申请全成或全败。
 * 约定：Consume 把 READY 会话原子置为 USED，返回 false 表示已被其他请求消费。
 */
type UploadRepository interface {
	CreateBatch(ctx context.Context, items []*entity.Upload) error
	GetByID(ctx context.Context, userID, uploadID string) (*entity.Upload, error)
	Update(ctx context.Context, upload *entity.Upload) error
	Consume(ctx context.Context, userID, uploadID string) (bool, error)
}

/* NotePageCursor 是页面列表的键集分页位置，按 Index 升序翻页 */
type NotePageCursor struct {
	Index int
	ID    string
}

/* NotePageQuery 描述一次页面分页查询 */
type NotePageQuery struct {
	UserID string
	NoteID string
	Limit  int
	After  *NotePageCursor
}

/*
 * NotePageRepository 管理笔记页面。
 * 约定：List 按 Index 升序；DeleteByNote 返回被删除的页面，供用例释放云端容量。
 */
type NotePageRepository interface {
	CreateBatch(ctx context.Context, pages []*entity.NotePage) error
	GetByID(ctx context.Context, userID, noteID, pageID string) (*entity.NotePage, error)
	List(ctx context.Context, query NotePageQuery) ([]*entity.NotePage, error)
	ListAll(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error)
	CountByNote(ctx context.Context, userID, noteID string) (int, error)
	CountByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]int, error)
	FirstByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]*entity.NotePage, error)
	DeleteByNote(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error)
}

/*
 * ConversionJobRepository 管理转换任务。
 * 约定：GetLatestByNote 返回该笔记最近一次任务；GetByID 供 Worker 按任务 ID 取数；
 * 单条查询一律带 userID（G-08）；Update 只在库中状态仍为 expected 时写入，
 * 返回是否生效，避免取消与成功互相覆盖。
 */
type ConversionJobRepository interface {
	Create(ctx context.Context, job *entity.ConversionJob) error
	GetLatestByNote(ctx context.Context, userID, noteID string) (*entity.ConversionJob, error)
	GetByID(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error)
	/* GetForUpdate 在事务内对任务行加锁读取，串行化并发状态迁移 */
	GetForUpdate(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error)
	/* Update 是状态机 CAS：expected 与库中状态不一致时不写入并返回 false */
	Update(ctx context.Context, job *entity.ConversionJob, expected entity.JobState) (bool, error)
	DeleteByNote(ctx context.Context, userID, noteID string) error
	/* ListActiveByUser 返回仍处于 QUEUED/RUNNING 的任务，供额度接口判断 conversionRunning */
	ListActiveByUser(ctx context.Context, userID string) ([]*entity.ConversionJob, error)
	/* ListPending 返回更新时间早于 before 的排队任务，供 Worker 恢复与补偿入队 */
	ListPending(ctx context.Context, before time.Time, limit int) ([]*entity.ConversionJob, error)
	/* ReleaseRunning 把上次进程中断遗留的 RUNNING 任务回落为 QUEUED；仅单实例启动可调用 */
	ReleaseRunning(ctx context.Context, now time.Time) (int, error)
}

/* 账本原因固定为两种，保证同一任务的返还流水幂等（unique(job_id, reason)） */
const (
	QuotaReasonCharge = "charge"
	QuotaReasonRefund = "refund"
)

/*
 * QuotaLedger 是日额度账本。
 * 约定：quotaDate 为北京时间日期（YYYY-MM-DD）；Charge 在余额不足时返回 QUOTA_EXCEEDED；
 * Refund 依据原扣减流水的日期入账，重复调用同一 jobID 不重复返还。
 */
type QuotaLedger interface {
	Remaining(ctx context.Context, userID, quotaDate string) (int, error)
	Charge(ctx context.Context, userID, jobID, quotaDate string, amount, limit int) error
	Refund(ctx context.Context, userID, jobID string, amount int) error
}

/*
 * StorageUsage 是云端容量计量。
 * 约定：Reserve 在 used+bytes 超过 limit 时返回 STORAGE_EXCEEDED；
 * Release 不得把用量减为负数；回收站内容继续占用容量，彻底删除才释放。
 */
type StorageUsage interface {
	Used(ctx context.Context, userID string) (int64, error)
	Reserve(ctx context.Context, userID string, bytes, limit int64) error
	Release(ctx context.Context, userID string, bytes int64) error
}

/*
 * ConversionQueue 是任务队列端口。
 * Enqueue 在生产态对应 Redis 可靠队列入队；Next 阻塞等待下一任务，返回任务归属账号与任务 ID，
 * ctx 取消或队列关闭时返回 ok=false。Ack 确认任务已处理完毕；
 * 未确认的任务由 Recover 在下次启动时重新入队，保证至少一次投递。
 */
type ConversionQueue interface {
	Enqueue(ctx context.Context, userID, jobID string) error
	Next(ctx context.Context) (userID string, jobID string, ok bool, err error)
	Ack(ctx context.Context, userID, jobID string) error
	Recover(ctx context.Context) error
}
