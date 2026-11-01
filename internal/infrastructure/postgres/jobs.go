/* 导出与批量导入任务的 PostgreSQL 实现 */
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* ExportJobRepository 是 port.ExportJobRepository 的 PostgreSQL 实现 */
type ExportJobRepository struct {
	db *DB
}

/* NewExportJobRepository 绑定连接池 */
func NewExportJobRepository(db *DB) *ExportJobRepository { return &ExportJobRepository{db: db} }

/* exportColumns 是读取导出任务的列清单 */
const exportColumns = `id, user_id, note_id, folder_id, format, state, object_key,
	failure_code, expires_at, created_at, finished_at`

/* Create 插入导出任务 */
func (r *ExportJobRepository) Create(ctx context.Context, job *entity.ExportJob) error {
	_, err := r.db.q(ctx).Exec(ctx, `
		INSERT INTO export_jobs (`+exportColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		job.ID, job.UserID, job.NoteID, job.FolderID, string(job.Format), string(job.State),
		job.ObjectKey, job.FailureCode, job.ExpiresAt, job.CreatedAt, job.FinishedAt)
	if err != nil {
		return fmt.Errorf("创建导出任务: %w", err)
	}
	return nil
}

/* GetByID 按属主查询导出任务 */
func (r *ExportJobRepository) GetByID(ctx context.Context, userID, exportID string) (*entity.ExportJob, error) {
	job, err := scanExportJob(r.db.q(ctx).QueryRow(ctx, `SELECT `+exportColumns+` FROM export_jobs
		WHERE user_id = $1 AND id = $2`, userID, exportID))
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "导出任务不存在")
	}
	return job, nil
}

/* Update 覆盖导出任务状态与结果 */
func (r *ExportJobRepository) Update(ctx context.Context, job *entity.ExportJob) error {
	tag, err := r.db.q(ctx).Exec(ctx, `
		UPDATE export_jobs SET state = $2, object_key = $3, failure_code = $4,
			expires_at = $5, finished_at = $6
		WHERE user_id = $7 AND id = $1`,
		job.ID, string(job.State), job.ObjectKey, job.FailureCode, job.ExpiresAt,
		job.FinishedAt, job.UserID)
	if err != nil {
		return fmt.Errorf("更新导出任务: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CodeResourceNotFound, "导出任务不存在")
	}
	return nil
}

/* scanExportJob 把一行映射为导出任务 */
func scanExportJob(row rowScanner) (*entity.ExportJob, error) {
	job := &entity.ExportJob{}
	var format, state string
	err := row.Scan(&job.ID, &job.UserID, &job.NoteID, &job.FolderID, &format, &state,
		&job.ObjectKey, &job.FailureCode, &job.ExpiresAt, &job.CreatedAt, &job.FinishedAt)
	if err != nil {
		return nil, err
	}
	job.Format = entity.ExportFormat(format)
	job.State = entity.ExportState(state)
	return job, nil
}

/* ImportJobRepository 是 port.ImportJobRepository 的 PostgreSQL 实现 */
type ImportJobRepository struct {
	db *DB
}

/* NewImportJobRepository 绑定连接池 */
func NewImportJobRepository(db *DB) *ImportJobRepository { return &ImportJobRepository{db: db} }

/* importColumns 是读取导入任务的列清单 */
const importColumns = `id, user_id, folder_id, mode, state, total, succeeded, failed,
	failure_code, created_at, finished_at`

/* Create 插入导入任务与逐项占位记录，位置即导入顺序 */
func (r *ImportJobRepository) Create(ctx context.Context, job *entity.ImportJob) error {
	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO import_jobs (`+importColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		job.ID, job.UserID, job.FolderID, string(job.Mode), string(job.State),
		job.Total, job.Succeeded, job.Failed, job.FailureCode, job.CreatedAt, job.FinishedAt)
	for index, item := range job.Items {
		batch.Queue(`
			INSERT INTO import_items (job_id, position, upload_id, note_id, state, failure_code)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			job.ID, index+1, item.UploadID, item.NoteID, string(item.State), item.FailureCode)
	}
	if err := r.db.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("创建导入任务: %w", err)
	}
	return nil
}

/* GetByID 读取导入任务与逐项结果 */
func (r *ImportJobRepository) GetByID(ctx context.Context, userID, importID string) (*entity.ImportJob, error) {
	job := &entity.ImportJob{}
	var mode, state string
	err := r.db.q(ctx).QueryRow(ctx, `SELECT `+importColumns+` FROM import_jobs
		WHERE user_id = $1 AND id = $2`, userID, importID).Scan(
		&job.ID, &job.UserID, &job.FolderID, &mode, &state, &job.Total, &job.Succeeded,
		&job.Failed, &job.FailureCode, &job.CreatedAt, &job.FinishedAt)
	if err != nil {
		return nil, mapNotFound(err, errs.CodeResourceNotFound, "导入任务不存在")
	}
	job.Mode = entity.ImportMode(mode)
	job.State = entity.ImportState(state)

	rows, err := r.db.q(ctx).Query(ctx, `
		SELECT upload_id, note_id, state, failure_code FROM import_items
		WHERE job_id = $1 ORDER BY position ASC`, importID)
	if err != nil {
		return nil, fmt.Errorf("查询导入项: %w", err)
	}
	defer rows.Close()

	items := make([]*entity.ImportItem, 0, job.Total)
	for rows.Next() {
		item := &entity.ImportItem{}
		var itemState string
		if err := rows.Scan(&item.UploadID, &item.NoteID, &itemState, &item.FailureCode); err != nil {
			return nil, fmt.Errorf("扫描导入项: %w", err)
		}
		item.State = entity.ImportItemState(itemState)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历导入项: %w", err)
	}
	job.Items = items
	return job, nil
}

/* Update 覆盖导入任务汇总状态与逐项结果 */
func (r *ImportJobRepository) Update(ctx context.Context, job *entity.ImportJob) error {
	batch := &pgx.Batch{}
	batch.Queue(`
		UPDATE import_jobs SET state = $2, total = $3, succeeded = $4, failed = $5,
			failure_code = $6, finished_at = $7
		WHERE user_id = $8 AND id = $1`,
		job.ID, string(job.State), job.Total, job.Succeeded, job.Failed,
		job.FailureCode, job.FinishedAt, job.UserID)
	for index, item := range job.Items {
		batch.Queue(`
			UPDATE import_items SET note_id = $3, state = $4, failure_code = $5
			WHERE job_id = $1 AND position = $2`,
			job.ID, index+1, item.NoteID, string(item.State), item.FailureCode)
	}
	if err := r.db.execBatch(ctx, batch); err != nil {
		return fmt.Errorf("更新导入任务: %w", err)
	}
	return nil
}

/* 确保实现端口契约 */
var (
	_ port.ExportJobRepository = (*ExportJobRepository)(nil)
	_ port.ImportJobRepository = (*ImportJobRepository)(nil)
)
