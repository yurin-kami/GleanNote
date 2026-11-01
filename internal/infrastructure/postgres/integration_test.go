/*
 * PostgreSQL 适配层集成测试（T-02）。
 *
 * 运行条件：设置 GN_TEST_DATABASE_URL 指向可丢弃的 PostgreSQL 16 实例，
 * 未设置时全部用例跳过，保证 `go test ./...` 在无外部依赖时仍然全绿。
 * 每个用例执行前清空 public schema 全部表，用例之间互不影响。
 */
package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* testDB 打开测试库、重置数据并登记关闭钩子 */
func testDB(t *testing.T) *DB {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("GN_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("未设置 GN_TEST_DATABASE_URL，跳过 PostgreSQL 集成测试")
	}
	db, err := Open(context.Background(), url, 4)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	truncateAll(t, db)
	return db
}

/* truncateAll 清空 public schema 下全部表；本 schema 无外键，可一次性截断 */
func truncateAll(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.pool.Query(ctx,
		`SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatalf("查询表清单失败: %v", err)
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描表名失败: %v", err)
		}
		if name == "schema_migrations" {
			continue
		}
		tables = append(tables, fmt.Sprintf(`"%s"`, name))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表清单失败: %v", err)
	}
	if len(tables) == 0 {
		return
	}
	if _, err := db.pool.Exec(ctx,
		"TRUNCATE TABLE "+strings.Join(tables, ", ")+" RESTART IDENTITY"); err != nil {
		t.Fatalf("截断测试表失败: %v", err)
	}
}

/* countRows 统计满足条件的行数，用于验证清理与保留 */
func countRows(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	return count
}

/* TestMigrateCreatesConcurrencyIndexes 验证 0002 迁移中的约束与索引已落地 */
func TestMigrateCreatesConcurrencyIndexes(t *testing.T) {
	db := testDB(t)
	wanted := []string{
		"ux_conversion_jobs_running",
		"ix_notes_purge_at",
		"ix_folders_purge_at",
		"ix_uploads_user_status",
	}
	for _, name := range wanted {
		if got := countRows(t, db, `SELECT COUNT(*) FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = $1`, name); got != 1 {
			t.Fatalf("索引 %s 未创建", name)
		}
	}
}

/* TestNoteRevisionCAS 验证 revision 条件更新：只有一次写入成功且失败方不产生事件 */
func TestNoteRevisionCAS(t *testing.T) {
	db := testDB(t)
	changes := NewChangeLogRepository(db)
	repo := NewNoteRepository(db, changes)
	ctx := context.Background()
	now := time.Now().UTC()

	note := &entity.Note{
		ID: "note-1", UserID: "user-1", FolderID: "folder-1", Title: "旧标题",
		Content: "旧正文", State: entity.NoteStateNormal, Revision: 1, CharCount: 3,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, note); err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}

	first := *note
	first.Title, first.Revision, first.UpdatedAt = "写入 A", 2, now.Add(time.Second)
	ok, err := repo.UpdateWithRevision(ctx, &first, 1)
	if err != nil || !ok {
		t.Fatalf("首次条件更新应成功: ok=%v err=%v", ok, err)
	}

	second := *note
	second.Title, second.Revision, second.UpdatedAt = "写入 B", 2, now.Add(2*time.Second)
	ok, err = repo.UpdateWithRevision(ctx, &second, 1)
	if err != nil {
		t.Fatalf("过期条件更新返回错误: %v", err)
	}
	if ok {
		t.Fatalf("过期条件更新不应成功")
	}

	stored, err := repo.GetByID(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("读取笔记失败: %v", err)
	}
	if stored.Title != "写入 A" || stored.Revision != 2 {
		t.Fatalf("数据库状态被覆盖: title=%s revision=%d", stored.Title, stored.Revision)
	}
	/* 创建 1 条 + 成功更新 1 条；失败的条件更新不得追加事件 */
	if got := countRows(t, db, `SELECT COUNT(*) FROM change_events WHERE user_id = 'user-1'`); got != 2 {
		t.Fatalf("变更事件数量应为 2，实际 %d", got)
	}
}

/* TestNoteRevisionCASConcurrent 验证并发同 revision 写入只有一个成功 */
func TestNoteRevisionCASConcurrent(t *testing.T) {
	db := testDB(t)
	repo := NewNoteRepository(db, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	note := &entity.Note{
		ID: "note-c", UserID: "user-1", FolderID: "folder-1", Title: "初始",
		State: entity.NoteStateNormal, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, note); err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			copied := *note
			copied.Title = fmt.Sprintf("并发写入-%d", index)
			copied.Revision = 2
			copied.UpdatedAt = now.Add(time.Duration(index+1) * time.Second)
			ok, err := repo.UpdateWithRevision(ctx, &copied, 1)
			if err != nil {
				t.Errorf("并发条件更新出错: %v", err)
				results <- false
				return
			}
			results <- ok
		}(i)
	}
	wg.Wait()
	close(results)

	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发条件更新应恰好一个成功，实际 %d", winners)
	}
}

/* TestFolderLockTreeRequiresTransaction 验证咨询锁只能在事务内使用 */
func TestFolderLockTreeRequiresTransaction(t *testing.T) {
	db := testDB(t)
	repo := NewFolderRepository(db, nil)
	if err := repo.LockTree(context.Background(), "user-1"); err != nil {
		if !errs.IsCode(err, errs.CodeInternal) {
			t.Fatalf("事务外锁树应返回内部错误，实际 %v", err)
		}
	} else {
		t.Fatalf("事务外调用 LockTree 应失败")
	}
}

/* TestFolderLockSerializesWriters 验证咨询锁把同账号目录写入串行化 */
func TestFolderLockSerializesWriters(t *testing.T) {
	db := testDB(t)
	repo := NewFolderRepository(db, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	folder := &entity.Folder{
		ID: "folder-1", UserID: "user-1", Name: "初始", Depth: 1,
		Path: "/folder-1", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Create(ctx, folder); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			err := db.WithinTransaction(ctx, func(txCtx context.Context) error {
				if err := repo.LockTree(txCtx, "user-1"); err != nil {
					return err
				}
				current, err := repo.GetByID(txCtx, "user-1", "folder-1")
				if err != nil {
					return err
				}
				current.Name = fmt.Sprintf("改名-%d", index)
				current.Revision++
				current.UpdatedAt = now.Add(time.Duration(index+1) * time.Second)
				ok, err := repo.UpdateWithRevision(txCtx, current, current.Revision-1)
				if err != nil {
					return err
				}
				if !ok {
					t.Errorf("持锁后条件更新仍失败")
				}
				return nil
			})
			if err != nil {
				t.Errorf("事务内改名失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	stored, err := repo.GetByID(ctx, "user-1", "folder-1")
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if stored.Revision != 3 {
		t.Fatalf("串行化后 revision 应为 3，实际 %d", stored.Revision)
	}
}

/* TestRefreshTokenRotationSingleWinner 验证并发轮换只有一个请求成功 */
func TestRefreshTokenRotationSingleWinner(t *testing.T) {
	db := testDB(t)
	repo := NewRefreshTokenRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	token := &entity.RefreshToken{
		ID: "rt-1", UserID: "user-1", TokenHash: "hash-1", DeviceID: "device-1",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := repo.Create(ctx, token); err != nil {
		t.Fatalf("创建刷新令牌失败: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			err := db.WithinTransaction(ctx, func(txCtx context.Context) error {
				current, err := repo.GetForUpdate(txCtx, "hash-1")
				if err != nil {
					return err
				}
				if !current.Usable(time.Now().UTC()) {
					return nil
				}
				current.Revoke(time.Now().UTC(), fmt.Sprintf("rt-new-%d", index))
				ok, err := repo.Update(txCtx, current)
				if err != nil {
					return err
				}
				if ok {
					mu.Lock()
					winners++
					mu.Unlock()
				}
				return nil
			})
			if err != nil {
				t.Errorf("轮换事务失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("并发轮换应恰好一个成功，实际 %d", winners)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM refresh_tokens
		WHERE id = 'rt-1' AND revoked_at IS NOT NULL`); got != 1 {
		t.Fatalf("令牌未被吊销")
	}
}

/* TestConversionJobUserSerial 验证唯一索引保证同账号只允许一个 RUNNING 任务 */
func TestConversionJobUserSerial(t *testing.T) {
	db := testDB(t)
	repo := NewConversionJobRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	first := entity.NewConversionJob("job-1", "note-1", "user-1", entity.JobKindCreate, 2, 2, 1, now)
	second := entity.NewConversionJob("job-2", "note-2", "user-1", entity.JobKindCreate, 2, 2, 1, now)
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("创建任务一失败: %v", err)
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("创建任务二失败: %v", err)
	}

	first.Start(now)
	ok, err := repo.Update(ctx, first, entity.JobStateQueued)
	if err != nil || !ok {
		t.Fatalf("任务一进入 RUNNING 失败: ok=%v err=%v", ok, err)
	}

	second.Start(now)
	if _, err := repo.Update(ctx, second, entity.JobStateQueued); err != nil {
		if !errs.IsCode(err, errs.CodeConversionStateInvalid) {
			t.Fatalf("同账号第二个 RUNNING 任务应返回状态错误，实际 %v", err)
		}
	} else {
		t.Fatalf("同账号第二个 RUNNING 任务不应成功")
	}

	/* 任务一结束后，任务二才能进入 RUNNING */
	first.Succeed(now.Add(time.Second))
	ok, err = repo.Update(ctx, first, entity.JobStateRunning)
	if err != nil || !ok {
		t.Fatalf("任务一成功迁移失败: ok=%v err=%v", ok, err)
	}
	second.State = entity.JobStateRunning
	second.Phase = entity.JobPhaseRecognizing
	second.UpdatedAt = now.Add(2 * time.Second)
	ok, err = repo.Update(ctx, second, entity.JobStateQueued)
	if err != nil || !ok {
		t.Fatalf("任务二在任务一结束后应可运行: ok=%v err=%v", ok, err)
	}

	/* 状态 CAS：已结束任务不能再次迁移 */
	ok, err = repo.Update(ctx, second, entity.JobStateQueued)
	if err != nil {
		t.Fatalf("过期状态 CAS 返回错误: %v", err)
	}
	if ok {
		t.Fatalf("过期状态 CAS 不应成功")
	}
}

/* TestUploadConsumeOnce 验证上传会话只能被消费一次且按属主隔离 */
func TestUploadConsumeOnce(t *testing.T) {
	db := testDB(t)
	repo := NewUploadRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	upload := entity.NewUpload("upload-1", "user-1", "obj-1", "p.png", "image/png", "sha", 10,
		now.Add(30*time.Minute), now)
	upload.MarkReady(100, 200, "thumb-1", "sha")
	if err := repo.CreateBatch(ctx, []*entity.Upload{upload}); err != nil {
		t.Fatalf("创建上传会话失败: %v", err)
	}

	ok, err := repo.Consume(ctx, "user-1", "upload-1")
	if err != nil || !ok {
		t.Fatalf("首次消费应成功: ok=%v err=%v", ok, err)
	}
	ok, err = repo.Consume(ctx, "user-1", "upload-1")
	if err != nil {
		t.Fatalf("重复消费返回错误: %v", err)
	}
	if ok {
		t.Fatalf("重复消费不应成功")
	}
	ok, err = repo.Consume(ctx, "user-2", "upload-1")
	if err != nil {
		t.Fatalf("跨账号消费返回错误: %v", err)
	}
	if ok {
		t.Fatalf("跨账号消费不应成功")
	}

	stored, err := repo.GetByID(ctx, "user-1", "upload-1")
	if err != nil {
		t.Fatalf("读取上传会话失败: %v", err)
	}
	if !stored.Consumed() {
		t.Fatalf("上传会话状态应为 USED，实际 %s", stored.Status)
	}
}

/* TestListExpiredPurge 验证回收站到期查询按 purge_at 过滤 */
func TestListExpiredPurge(t *testing.T) {
	db := testDB(t)
	notes := NewNoteRepository(db, nil)
	folders := NewFolderRepository(db, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)

	for _, item := range []struct {
		id       string
		purgeAt  *time.Time
		deleted  bool
		expected bool
	}{
		{"note-expired", &past, true, true},
		{"note-future", ptrTime(now.Add(time.Hour)), true, false},
		{"note-active", nil, false, false},
	} {
		note := &entity.Note{
			ID: item.id, UserID: "user-1", FolderID: "folder-1", Title: item.id,
			State: entity.NoteStateNormal, Revision: 1, CreatedAt: now, UpdatedAt: now,
			PurgeAt: item.purgeAt,
		}
		if item.deleted {
			note.State = entity.NoteStateDeleted
			note.DeletedAt = &past
		}
		if err := notes.Create(ctx, note); err != nil {
			t.Fatalf("创建笔记 %s 失败: %v", item.id, err)
		}
	}
	expired, err := notes.ListExpired(ctx, now, 10)
	if err != nil {
		t.Fatalf("查询到期笔记失败: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != "note-expired" {
		t.Fatalf("到期笔记查询结果错误: %+v", expired)
	}

	folder := &entity.Folder{
		ID: "folder-expired", UserID: "user-1", Name: "到期目录", Depth: 1,
		Path: "/folder-expired", Revision: 1, CreatedAt: now, UpdatedAt: now,
		DeletedAt: &past, PurgeAt: &past,
	}
	if err := folders.Create(ctx, folder); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	expiredFolders, err := folders.ListExpired(ctx, now, 10)
	if err != nil {
		t.Fatalf("查询到期目录失败: %v", err)
	}
	if len(expiredFolders) != 1 || expiredFolders[0].ID != "folder-expired" {
		t.Fatalf("到期目录查询结果错误: %+v", expiredFolders)
	}
}

/* ptrTime 返回时间指针，便于构造可空列 */
func ptrTime(value time.Time) *time.Time { return &value }

/*
 * TestAccountPurgerRemovesAllTables 验证注销清理覆盖全部业务表且不波及他人数据。
 * 账号表本身不在此端口删除，由认证用例在清理完成后单独调用。
 */
func TestAccountPurgerRemovesAllTables(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedAccountData(t, db, "user-a", "a@example.com", now)
	seedAccountData(t, db, "user-b", "b@example.com", now)

	purger := NewAccountPurger(db)
	if err := db.WithinTransaction(ctx, func(txCtx context.Context) error {
		return purger.PurgeForUser(txCtx, "user-a")
	}); err != nil {
		t.Fatalf("清理账号数据失败: %v", err)
	}

	for _, check := range purgeChecks {
		if got := countRows(t, db, check.sql, "user-a"); got != 0 {
			t.Fatalf("清理后 %s 仍残留 %d 行", check.name, got)
		}
		if got := countRows(t, db, check.sql, "user-b"); got != 1 {
			t.Fatalf("误删他人数据 %s: 剩余 %d 行", check.name, got)
		}
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM users WHERE id = 'user-a'`); got != 1 {
		t.Fatalf("Purger 不应删除 users 行")
	}
}

/* purgeChecks 是逐表清理校验：参数 1 为用户 ID */
var purgeChecks = []struct {
	name string
	sql  string
}{
	{"note_versions", `SELECT COUNT(*) FROM note_versions WHERE user_id = $1`},
	{"share_links", `SELECT COUNT(*) FROM share_links WHERE user_id = $1`},
	{"note_tags", `SELECT COUNT(*) FROM note_tags WHERE user_id = $1`},
	{"conversion_jobs", `SELECT COUNT(*) FROM conversion_jobs WHERE user_id = $1`},
	{"note_pages", `SELECT COUNT(*) FROM note_pages WHERE user_id = $1`},
	{"quota_charges", `SELECT COUNT(*) FROM quota_charges WHERE user_id = $1`},
	{"quota_usage", `SELECT COUNT(*) FROM quota_usage WHERE user_id = $1`},
	{"storage_usage", `SELECT COUNT(*) FROM storage_usage WHERE user_id = $1`},
	{"uploads", `SELECT COUNT(*) FROM uploads WHERE user_id = $1`},
	{"export_jobs", `SELECT COUNT(*) FROM export_jobs WHERE user_id = $1`},
	{"import_items", `SELECT COUNT(*) FROM import_items WHERE job_id IN
		(SELECT id FROM import_jobs WHERE user_id = $1)`},
	{"import_jobs", `SELECT COUNT(*) FROM import_jobs WHERE user_id = $1`},
	{"change_events", `SELECT COUNT(*) FROM change_events WHERE user_id = $1`},
	{"change_seq", `SELECT COUNT(*) FROM change_seq WHERE user_id = $1`},
	{"change_watermarks", `SELECT COUNT(*) FROM change_watermarks WHERE user_id = $1`},
	{"refresh_tokens", `SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`},
	{"notes", `SELECT COUNT(*) FROM notes WHERE user_id = $1`},
	{"folders", `SELECT COUNT(*) FROM folders WHERE user_id = $1`},
	{"tags", `SELECT COUNT(*) FROM tags WHERE user_id = $1`},
	{"email_codes", `SELECT COUNT(*) FROM email_codes WHERE lower(email) =
		(SELECT lower(email) FROM users WHERE id = $1)`},
}

/* seedAccountData 为指定账号插入每个业务表各一行，供清理覆盖度校验 */
func seedAccountData(t *testing.T, db *DB, userID, email string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	noteID := "note-" + userID
	folderID := "folder-" + userID
	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{"users", `INSERT INTO users (id, email, password_hash, nickname, status, created_at, updated_at)
			VALUES ($1, $2, 'hash', '昵称', 'ACTIVE', $3, $3)`, []any{userID, email, now}},
		{"folders", `INSERT INTO folders (id, user_id, name, depth, path, revision, created_at, updated_at)
			VALUES ($1, $2, '目录', 1, $3, 1, $4, $4)`,
			[]any{folderID, userID, "/" + folderID, now}},
		{"notes", `INSERT INTO notes (id, user_id, folder_id, title, content, state, revision,
			char_count, created_at, updated_at)
			VALUES ($1, $2, $3, '标题', '正文', 'NORMAL', 1, 2, $4, $4)`,
			[]any{noteID, userID, folderID, now}},
		{"note_pages", `INSERT INTO note_pages (id, note_id, user_id, page_index, object_key,
			bytes, width, height, created_at)
			VALUES ($1, $2, $3, 0, 'obj', 10, 100, 200, $4)`,
			[]any{"page-" + userID, noteID, userID, now}},
		{"uploads", `INSERT INTO uploads (id, user_id, object_key, file_name, content_type,
			bytes, status, expires_at, created_at)
			VALUES ($1, $2, 'obj', 'p.png', 'image/png', 10, 'READY', $3, $4)`,
			[]any{"upload-" + userID, userID, now.Add(time.Hour), now}},
		{"conversion_jobs", `INSERT INTO conversion_jobs (id, note_id, user_id, kind, state,
			phase, total_pages, created_at, updated_at)
			VALUES ($1, $2, $3, 'CREATE', 'SUCCEEDED', 'COMPLETED', 1, $4, $4)`,
			[]any{"job-" + userID, noteID, userID, now}},
		{"quota_usage", `INSERT INTO quota_usage (user_id, quota_date, used) VALUES ($1, '2026-01-01', 1)`,
			[]any{userID}},
		{"quota_charges", `INSERT INTO quota_charges (user_id, job_id, quota_date, amount, created_at)
			VALUES ($1, $2, '2026-01-01', 1, $3)`, []any{userID, "job-" + userID, now}},
		{"storage_usage", `INSERT INTO storage_usage (user_id, used_bytes) VALUES ($1, 10)`,
			[]any{userID}},
		{"tags", `INSERT INTO tags (id, user_id, name, created_at, updated_at)
			VALUES ($1, $2, '标签', $3, $3)`, []any{"tag-" + userID, userID, now}},
		{"note_tags", `INSERT INTO note_tags (user_id, note_id, tag_id) VALUES ($1, $2, $3)`,
			[]any{userID, noteID, "tag-" + userID}},
		{"note_versions", `INSERT INTO note_versions (id, user_id, note_id, source, content,
			char_count, created_at, expires_at)
			VALUES ($1, $2, $3, 'AUTO_SAVE', '正文', 2, $4, $5)`,
			[]any{"ver-" + userID, userID, noteID, now, now.Add(time.Hour)}},
		{"share_links", `INSERT INTO share_links (user_id, note_id, token, enabled, created_at, updated_at)
			VALUES ($1, $2, $3, TRUE, $4, $4)`, []any{userID, noteID, "token-" + userID, now}},
		{"export_jobs", `INSERT INTO export_jobs (id, user_id, format, state, created_at)
			VALUES ($1, $2, 'PDF', 'DONE', $3)`, []any{"exp-" + userID, userID, now}},
		{"import_jobs", `INSERT INTO import_jobs (id, user_id, folder_id, mode, state, total, created_at)
			VALUES ($1, $2, $3, 'APPEND', 'DONE', 1, $4)`, []any{"imp-" + userID, userID, folderID, now}},
		{"import_items", `INSERT INTO import_items (job_id, position, upload_id, state)
			VALUES ($1, 0, $2, 'DONE')`, []any{"imp-" + userID, "upload-" + userID}},
		{"change_events", `INSERT INTO change_events (user_id, seq, resource_type, resource_id,
			op, updated_at) VALUES ($1, 1, 'note', $2, 'CREATE', $3)`, []any{userID, noteID, now}},
		{"change_seq", `INSERT INTO change_seq (user_id, next_seq) VALUES ($1, 1)`, []any{userID}},
		{"change_watermarks", `INSERT INTO change_watermarks (user_id, trimmed_through) VALUES ($1, 0)`,
			[]any{userID}},
		{"refresh_tokens", `INSERT INTO refresh_tokens (id, user_id, token_hash, device_id,
			expires_at, created_at) VALUES ($1, $2, $3, 'device', $4, $5)`,
			[]any{"rt-" + userID, userID, "hash-" + userID, now.Add(time.Hour), now}},
		{"email_codes", `INSERT INTO email_codes (id, email, purpose, code_hash, expires_at, created_at)
			VALUES ($1, $2, 'REGISTER', 'code', $3, $4)`,
			[]any{"code-" + userID, email, now.Add(time.Minute), now}},
	}
	for _, statement := range statements {
		if _, err := db.pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("插入 %s 失败: %v", statement.name, err)
		}
	}
}
