/* 上传、页面、转换任务、额度与容量的 PostgreSQL 实现 */
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* UploadRepository 是 port.UploadRepository 的 PostgreSQL 实现 */
type UploadRepository struct {
	db *DB
}

/* NewUploadRepository 绑定连接池 */
func NewUploadRepository(db *DB) *UploadRepository { return &UploadRepository{db: db} }

/* uploadColumns 是读取上传会话的列清单 */
const uploadColumns = `id, user_id, object_key, file_name, content_type, sha256, bytes,
	width, height, thumb_key, status, expires_at, created_at`

/* CreateBatch 批量插入上传会话；一次申请全成或全败 */
func (r *UploadRepository) CreateBatch(ctx context.Context, items []*entity.Upload) error {
	if len(items) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, item := range items {
		batch.Queue(`
			INSERT INTO uploads (`+uploadColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			item.ID, item.UserID, item.ObjectKey, item.FileName, item.ContentType, item.SHA256,
			item.Bytes, item.Width, item.Height, item.ThumbKey, string(item.Status),
			item.ExpiresAt, item.CreatedAt)
	}
	if err := r.db.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("创建上传会话: %w", err)
	}
	return nil
}

/* GetByID 按属主查询上传会话 */
func (r *UploadRepository) GetByID(ctx context.Context, userID, uploadID string) (*entity.Upload, error) {
	upload := &entity.Upload{}
	var status string
	err := r.db.q(ctx).QueryRow(ctx, `SELECT `+uploadColumns+` FROM uploads
		WHERE user_id = $1 AND id = $2`, userID, uploadID).Scan(
		&upload.ID, &upload.UserID, &upload.ObjectKey, &upload.FileName, &upload.ContentType,
		&upload.SHA256, &upload.Bytes, &upload.Width, &upload.Height, &upload.ThumbKey,
		&status, &upload.ExpiresAt, &upload.CreatedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeUploadNotFound, "上传会话不存在")
	}
	upload.Status = entity.UploadStatus(status)
	return upload, nil
}

/* Update 覆盖上传会话的校验结果与状态 */
func (r *UploadRepository) Update(ctx context.Context, upload *entity.Upload) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE uploads SET sha256 = $3, bytes = $4, width = $5, height = $6,
			thumb_key = $7, status = $8, expires_at = $9
		WHERE user_id = $1 AND id = $2`,
		upload.UserID, upload.ID, upload.SHA256, upload.Bytes, upload.Width, upload.Height,
		upload.ThumbKey, string(upload.Status), upload.ExpiresAt)
	if err != nil {
		return fmt.Errorf("更新上传会话: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CodeUploadNotFound, "上传会话不存在")
	}
	return nil
}

/* Consume 把 READY 会话原子置为 USED；返回 false 表示会话已被消费或不属于该用户 */
func (r *UploadRepository) Consume(ctx context.Context, userID, uploadID string) (bool, error) {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE uploads SET status = $3
		WHERE user_id = $1 AND id = $2 AND status = $4`,
		userID, uploadID, string(entity.UploadStatusUsed), string(entity.UploadStatusReady))
	if err != nil {
		return false, fmt.Errorf("消费上传会话: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

/* NotePageRepository 是 port.NotePageRepository 的 PostgreSQL 实现 */
type NotePageRepository struct {
	db      *DB
	changes *ChangeLogRepository
}

/* NewNotePageRepository 绑定连接池与变更日志 */
func NewNotePageRepository(db *DB, changes *ChangeLogRepository) *NotePageRepository {
	return &NotePageRepository{db: db, changes: changes}
}

/* pageColumns 是读取页面的列清单 */
const pageColumns = `id, note_id, user_id, page_index, object_key, thumb_key, bytes,
	width, height, sha256, created_at`

/* CreateBatch 批量插入页面并逐页追加同步事件 */
func (r *NotePageRepository) CreateBatch(ctx context.Context, pages []*entity.NotePage) error {
	if len(pages) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, page := range pages {
		batch.Queue(`
			INSERT INTO note_pages (`+pageColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			page.ID, page.NoteID, page.UserID, page.Index, page.ObjectKey, page.ThumbKey,
			page.Bytes, page.Width, page.Height, page.SHA256, page.CreatedAt)
	}
	if err := r.db.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("创建笔记页面: %w", err)
	}
	for _, page := range pages {
		if r.changes == nil {
			break
		}
		if _, err := r.changes.appendEvent(ctx, pageChangeEvent(page, page.CreatedAt)); err != nil {
			return err
		}
	}
	return nil
}

/* GetByID 按属主与笔记查询页面 */
func (r *NotePageRepository) GetByID(ctx context.Context, userID, noteID, pageID string) (*entity.NotePage, error) {
	page, err := scanPage(r.db.q(ctx).QueryRow(ctx, `SELECT `+pageColumns+` FROM note_pages
		WHERE user_id = $1 AND note_id = $2 AND id = $3`, userID, noteID, pageID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "页面不存在")
	}
	return page, nil
}

/* List 按 Index 升序返回页面，支持游标翻页 */
func (r *NotePageRepository) List(ctx context.Context, query port.NotePageQuery) ([]*entity.NotePage, error) {
	statement := `SELECT ` + pageColumns + ` FROM note_pages WHERE user_id = $1 AND note_id = $2`
	args := []any{query.UserID, query.NoteID}
	if cursor := query.After; cursor != nil {
		statement += ` AND (page_index > $3 OR (page_index = $3 AND id > $4))`
		args = append(args, cursor.Index, cursor.ID)
	}
	statement += ` ORDER BY page_index ASC, id ASC`
	if query.Limit > 0 {
		statement += fmt.Sprintf(` LIMIT %d`, query.Limit)
	}
	return r.queryPages(ctx, statement, args...)
}

/* ListAll 返回笔记全部页面，供转换与导出使用 */
func (r *NotePageRepository) ListAll(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error) {
	return r.queryPages(ctx, `SELECT `+pageColumns+` FROM note_pages
		WHERE user_id = $1 AND note_id = $2 ORDER BY page_index ASC, id ASC`, userID, noteID)
}

/* CountByNote 统计笔记页面数 */
func (r *NotePageRepository) CountByNote(ctx context.Context, userID, noteID string) (int, error) {
	var count int
	if err := r.db.q(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM note_pages
		WHERE user_id = $1 AND note_id = $2`, userID, noteID).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计笔记页面: %w", err)
	}
	return count, nil
}

/* CountByNotes 批量统计多篇笔记的页面数 */
func (r *NotePageRepository) CountByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(noteIDs))
	if len(noteIDs) == 0 {
		return counts, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT note_id, COUNT(*) FROM note_pages
		WHERE user_id = $1 AND note_id = ANY($2) GROUP BY note_id`, userID, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("批量统计笔记页面: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var noteID string
		var count int
		if err := rows.Scan(&noteID, &count); err != nil {
			return nil, fmt.Errorf("扫描页面计数: %w", err)
		}
		counts[noteID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历页面计数: %w", err)
	}
	return counts, nil
}

/* FirstByNotes 批量返回每篇笔记首页，供列表展示封面 */
func (r *NotePageRepository) FirstByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]*entity.NotePage, error) {
	pages := make(map[string]*entity.NotePage, len(noteIDs))
	if len(noteIDs) == 0 {
		return pages, nil
	}
	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT DISTINCT ON (note_id) `+pageColumns+` FROM note_pages
		WHERE user_id = $1 AND note_id = ANY($2)
		ORDER BY note_id, page_index ASC`, userID, noteIDs)
	if err != nil {
		return nil, fmt.Errorf("批量查询首页: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		page, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描首页: %w", err)
		}
		pages[page.NoteID] = page
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历首页: %w", err)
	}
	return pages, nil
}

/* DeleteByNote 删除笔记全部页面并返回被删除行，供用例释放云端容量 */
func (r *NotePageRepository) DeleteByNote(ctx context.Context, userID, noteID string) ([]*entity.NotePage, error) {
	rows, err := r.db.q(ctx).Query(ctx, `DELETE FROM note_pages
		WHERE user_id = $1 AND note_id = $2 RETURNING `+pageColumns, userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("删除笔记页面: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.NotePage, 0)
	for rows.Next() {
		page, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描已删除页面: %w", err)
		}
		items = append(items, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历已删除页面: %w", err)
	}
	return items, nil
}

/* queryPages 执行查询并映射页面实体 */
func (r *NotePageRepository) queryPages(ctx context.Context, query string, args ...any) ([]*entity.NotePage, error) {
	rows, err := r.db.q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询笔记页面: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.NotePage, 0)
	for rows.Next() {
		page, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描笔记页面: %w", err)
		}
		items = append(items, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历笔记页面: %w", err)
	}
	return items, nil
}

/* scanPage 把一行映射为页面实体 */
func scanPage(row rowScanner) (*entity.NotePage, error) {
	page := &entity.NotePage{}
	err := row.Scan(&page.ID, &page.NoteID, &page.UserID, &page.Index, &page.ObjectKey,
		&page.ThumbKey, &page.Bytes, &page.Width, &page.Height, &page.SHA256, &page.CreatedAt)
	if err != nil {
		return nil, err
	}
	return page, nil
}

/* ConversionJobRepository 是 port.ConversionJobRepository 的 PostgreSQL 实现 */
type ConversionJobRepository struct {
	db *DB
}

/* NewConversionJobRepository 绑定连接池 */
func NewConversionJobRepository(db *DB) *ConversionJobRepository {
	return &ConversionJobRepository{db: db}
}

/* jobColumns 是读取转换任务的列清单 */
const jobColumns = `id, note_id, user_id, kind, state, phase, total_pages, processed_pages,
	failure_code, failure_user_message, quota_charged, attempt, created_at, updated_at,
	started_at, finished_at`

/* Create 插入转换任务；同一账号已有排队/处理中任务时返回状态错误 */
func (r *ConversionJobRepository) Create(ctx context.Context, job *entity.ConversionJob) error {
	_, err := r.db.q(ctx).Exec(ctx, `INSERT INTO conversion_jobs (`+jobColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		job.ID, job.NoteID, job.UserID, string(job.Kind), string(job.State), string(job.Phase),
		job.TotalPages, job.ProcessedPages, string(job.FailureCode), job.FailureUserMessage,
		job.QuotaCharged, job.Attempt, job.CreatedAt, job.UpdatedAt, job.StartedAt, job.FinishedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return errs.New(errs.CodeConversionStateInvalid, "已有进行中的转换任务，请稍后再试")
		}
		return fmt.Errorf("创建转换任务: %w", err)
	}
	return nil
}

/* GetLatestByNote 返回该笔记最近一次任务 */
func (r *ConversionJobRepository) GetLatestByNote(ctx context.Context, userID, noteID string) (*entity.ConversionJob, error) {
	job, err := scanJob(r.db.q(ctx).QueryRow(ctx, `SELECT `+jobColumns+` FROM conversion_jobs
		WHERE user_id = $1 AND note_id = $2 ORDER BY created_at DESC LIMIT 1`, userID, noteID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeConversionNotFound, "转换任务不存在")
	}
	return job, nil
}

/* GetByID 按属主与任务 ID 取数，供 Worker 执行 */
func (r *ConversionJobRepository) GetByID(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error) {
	job, err := scanJob(r.db.q(ctx).QueryRow(ctx, `SELECT `+jobColumns+`
		FROM conversion_jobs WHERE user_id = $1 AND id = $2`, userID, jobID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeConversionNotFound, "转换任务不存在")
	}
	return job, nil
}

/* GetForUpdate 在事务内对任务行加锁读取，供状态迁移串行化 */
func (r *ConversionJobRepository) GetForUpdate(ctx context.Context, userID, jobID string) (*entity.ConversionJob, error) {
	if _, ok := txFrom(ctx); !ok {
		return nil, errs.New(errs.CodeInternal, "转换任务加锁读取必须在事务内")
	}
	job, err := scanJob(r.db.q(ctx).QueryRow(ctx, `SELECT `+jobColumns+`
		FROM conversion_jobs WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, jobID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeConversionNotFound, "转换任务不存在")
	}
	return job, nil
}

/* Update 覆盖任务状态机字段；状态或属主不匹配时不写入并返回 false */
func (r *ConversionJobRepository) Update(ctx context.Context, job *entity.ConversionJob,
	expected entity.JobState) (bool, error) {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE conversion_jobs SET state = $2, phase = $3, processed_pages = $4,
			failure_code = $5, failure_user_message = $6, quota_charged = $7, attempt = $8,
			updated_at = $9, started_at = $10, finished_at = $11
		WHERE id = $1 AND user_id = $13 AND state = $12`,
		job.ID, string(job.State), string(job.Phase), job.ProcessedPages,
		string(job.FailureCode), job.FailureUserMessage, job.QuotaCharged, job.Attempt,
		job.UpdatedAt, job.StartedAt, job.FinishedAt, string(expected), job.UserID)
	if err != nil {
		if isUniqueViolation(err) {
			return false, errs.New(errs.CodeConversionStateInvalid, "该账号已有进行中的转换任务")
		}
		return false, fmt.Errorf("更新转换任务: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

/* DeleteByNote 删除笔记的全部转换任务 */
func (r *ConversionJobRepository) DeleteByNote(ctx context.Context, userID, noteID string) error {
	if _, err := r.db.q(ctx).Exec(ctx, `DELETE FROM conversion_jobs
		WHERE user_id = $1 AND note_id = $2`, userID, noteID); err != nil {
		return fmt.Errorf("删除转换任务: %w", err)
	}
	return nil
}

/* ListActiveByUser 返回仍处于 QUEUED/RUNNING 的任务 */
func (r *ConversionJobRepository) ListActiveByUser(ctx context.Context, userID string) ([]*entity.ConversionJob, error) {
	rows, err := r.db.q(ctx).Query(ctx, `SELECT `+jobColumns+` FROM conversion_jobs
		WHERE user_id = $1 AND state IN ('QUEUED','RUNNING') ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询进行中任务: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.ConversionJob, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描进行中任务: %w", err)
		}
		items = append(items, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历进行中任务: %w", err)
	}
	return items, nil
}

/* ListPending 返回更新时间早于 before 的排队任务，按创建时间升序供 Worker 补偿入队 */
func (r *ConversionJobRepository) ListPending(ctx context.Context, before time.Time,
	limit int) ([]*entity.ConversionJob, error) {
	statement := `SELECT ` + jobColumns + ` FROM conversion_jobs
		WHERE state = 'QUEUED' AND updated_at <= $1 ORDER BY created_at ASC, id ASC`
	args := []any{before}
	if limit > 0 {
		statement += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := r.db.q(ctx).Query(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("查询排队任务: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.ConversionJob, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描排队任务: %w", err)
		}
		items = append(items, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历排队任务: %w", err)
	}
	return items, nil
}

/*
 * ReleaseRunning 把上次进程中断遗留的处理中任务回落为排队，返回受影响行数。
 * 约束：当前部署为单实例，只能在 Worker 启动时调用；横向扩容前需改为按实例租约/标识回收，
 * 否则任一实例启动都会回落其他实例的在途任务，造成重复识图与重复计费。
 */
func (r *ConversionJobRepository) ReleaseRunning(ctx context.Context, now time.Time) (int, error) {
	tag, err := r.db.q(ctx).Exec(ctx, `UPDATE conversion_jobs
		SET state = 'QUEUED', phase = 'QUEUED', started_at = NULL, updated_at = $1
		WHERE state = 'RUNNING'`, now)
	if err != nil {
		return 0, fmt.Errorf("回落处理中任务: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

/* scanJob 把一行映射为转换任务 */
func scanJob(row rowScanner) (*entity.ConversionJob, error) {
	job := &entity.ConversionJob{}
	var kind, state, phase, failureCode string
	err := row.Scan(&job.ID, &job.NoteID, &job.UserID, &kind, &state, &phase,
		&job.TotalPages, &job.ProcessedPages, &failureCode, &job.FailureUserMessage,
		&job.QuotaCharged, &job.Attempt, &job.CreatedAt, &job.UpdatedAt,
		&job.StartedAt, &job.FinishedAt)
	if err != nil {
		return nil, err
	}
	job.Kind = entity.JobKind(kind)
	job.State = entity.JobState(state)
	job.Phase = entity.JobPhase(phase)
	job.FailureCode = entity.JobFailureCode(failureCode)
	return job, nil
}

/* QuotaLedger 是 port.QuotaLedger 的 PostgreSQL 实现 */
type QuotaLedger struct {
	db *DB
}

/* NewQuotaLedger 绑定连接池 */
func NewQuotaLedger(db *DB) *QuotaLedger { return &QuotaLedger{db: db} }

/* Remaining 返回指定北京时间日期的剩余额度；已用超出上限时返回 0 */
func (l *QuotaLedger) Remaining(ctx context.Context, userID, quotaDate string) (int, error) {
	var used int
	err := l.db.q(ctx).QueryRow(ctx, `
		SELECT used FROM quota_usage WHERE user_id = $1 AND quota_date = $2`,
		userID, quotaDate).Scan(&used)
	if err != nil {
		if isNoRows(err) {
			return valueobject.DailyPageQuota, nil
		}
		return 0, fmt.Errorf("查询剩余额度: %w", err)
	}
	remaining := valueobject.DailyPageQuota - used
	if remaining < 0 {
		return 0, nil
	}
	return remaining, nil
}

/*
 * Charge 从当日额度扣减 amount。
 * 同一 jobID 重复扣减视为幂等成功；余额不足返回 QUOTA_EXCEEDED 并撤销已写入的流水。
 */
func (l *QuotaLedger) Charge(ctx context.Context, userID, jobID, quotaDate string, amount, limit int) error {
	tag, err := l.db.q(ctx).Exec(ctx, `
		INSERT INTO quota_charges (user_id, job_id, quota_date, amount, created_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (user_id, job_id) DO NOTHING`,
		userID, jobID, quotaDate, amount, nowFunc())
	if err != nil {
		return fmt.Errorf("写入额度流水: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}

	var used int
	/* $3 与 $4 必须显式标注 int，否则列类型（int4）与 Go int（int8）会让 PG 推断冲突（42P08） */
	err = l.db.q(ctx).QueryRow(ctx, `
		INSERT INTO quota_usage (user_id, quota_date, used)
		SELECT $1, $2, $3::int WHERE $3::int <= $4::int
		ON CONFLICT (user_id, quota_date) DO UPDATE
			SET used = quota_usage.used + $3::int
			WHERE quota_usage.used + $3::int <= $4::int
		RETURNING used`, userID, quotaDate, amount, limit).Scan(&used)
	if err != nil {
		if isNoRows(err) {
			_, _ = l.db.q(ctx).Exec(ctx,
				`DELETE FROM quota_charges WHERE user_id = $1 AND job_id = $2`, userID, jobID)
			return errs.New(errs.CodeQuotaExceeded, "今日额度不足")
		}
		return fmt.Errorf("扣减额度: %w", err)
	}
	return nil
}

/* Refund 依据原扣减流水返还额度；同一 jobID 重复返还是幂等成功 */
func (l *QuotaLedger) Refund(ctx context.Context, userID, jobID string, amount int) error {
	var quotaDate string
	var charged int
	err := l.db.q(ctx).QueryRow(ctx, `
		UPDATE quota_charges SET refunded_at = $3
		WHERE user_id = $1 AND job_id = $2 AND refunded_at IS NULL
		RETURNING quota_date, amount`, userID, jobID, nowFunc()).Scan(&quotaDate, &charged)
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return fmt.Errorf("返还额度: %w", err)
	}
	_, err = l.db.q(ctx).Exec(ctx, `
		UPDATE quota_usage SET used = GREATEST(used - $3, 0)
		WHERE user_id = $1 AND quota_date = $2`, userID, quotaDate, charged)
	if err != nil {
		return fmt.Errorf("回滚额度用量: %w", err)
	}
	return nil
}

/* StorageUsage 是 port.StorageUsage 的 PostgreSQL 实现 */
type StorageUsage struct {
	db *DB
}

/* NewStorageUsage 绑定连接池 */
func NewStorageUsage(db *DB) *StorageUsage { return &StorageUsage{db: db} }

/* Used 返回用户已占用的原图字节数 */
func (u *StorageUsage) Used(ctx context.Context, userID string) (int64, error) {
	var used int64
	err := u.db.q(ctx).QueryRow(ctx,
		`SELECT used_bytes FROM storage_usage WHERE user_id = $1`, userID).Scan(&used)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("查询云端用量: %w", err)
	}
	return used, nil
}

/* Reserve 预占容量；超过 limit 返回 STORAGE_EXCEEDED */
func (u *StorageUsage) Reserve(ctx context.Context, userID string, bytes, limit int64) error {
	var used int64
	/* 与额度扣减同理：显式标注 bigint，避免列类型与参数类型推断冲突（42P08） */
	err := u.db.q(ctx).QueryRow(ctx, `
		INSERT INTO storage_usage (user_id, used_bytes)
		SELECT $1, $2::bigint WHERE $2::bigint <= $3::bigint
		ON CONFLICT (user_id) DO UPDATE SET used_bytes = storage_usage.used_bytes + $2::bigint
			WHERE storage_usage.used_bytes + $2::bigint <= $3::bigint
		RETURNING used_bytes`, userID, bytes, limit).Scan(&used)
	if err != nil {
		if isNoRows(err) {
			return errs.New(errs.CodeStorageExceeded, "云端空间不足")
		}
		return fmt.Errorf("预占容量: %w", err)
	}
	return nil
}

/* Release 释放容量；不会把已用量减为负数 */
func (u *StorageUsage) Release(ctx context.Context, userID string, bytes int64) error {
	if _, err := u.db.q(ctx).Exec(ctx, `
		INSERT INTO storage_usage (user_id, used_bytes) VALUES ($1, 0)
		ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return fmt.Errorf("初始化容量记录: %w", err)
	}
	if _, err := u.db.q(ctx).Exec(ctx, `
		UPDATE storage_usage SET used_bytes = GREATEST(used_bytes - $2, 0)
		WHERE user_id = $1`, userID, bytes); err != nil {
		return fmt.Errorf("释放容量: %w", err)
	}
	return nil
}

/* 确保实现端口契约 */
var (
	_ port.UploadRepository        = (*UploadRepository)(nil)
	_ port.NotePageRepository      = (*NotePageRepository)(nil)
	_ port.ConversionJobRepository = (*ConversionJobRepository)(nil)
	_ port.QuotaLedger             = (*QuotaLedger)(nil)
	_ port.StorageUsage            = (*StorageUsage)(nil)
)
