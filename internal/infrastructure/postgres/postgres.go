/*
 * Package postgres 是业务持久化的 PostgreSQL 16 适配器。
 *
 * 职责：提供连接池、事务边界与各仓储的 SQL 实现；迁移脚本随二进制内嵌并在启动时应用。
 * 不负责：领域规则与业务校验；仓储只做数据映射，语义由端口契约约束。
 * 依赖：pgx/v5、domain 实体与端口、platform/clock。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/CONVENTIONS.md#31-通用 G-08
 */
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* migrations 内嵌全部 DDL，保证镜像不依赖仓库外的脚本目录 */
//
//go:embed migrations/*.sql
var migrations embed.FS

/* txKey 是事务上下文键，仓储据此复用同一连接 */
type txKey struct{}

/* nowFunc 统一仓储取时入口，便于测试替换；生产固定返回 UTC 系统时间（G-09） */
var nowFunc = func() time.Time { return time.Now().UTC() }

/*
 * querier 抽象连接池与事务的连接方法。
 * 仓储只依赖该接口，事务内自动落到 pgx.Tx，事务外落到连接池。
 */
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
}

/* DB 持有连接池并实现 port.Transactor */
type DB struct {
	pool *pgxpool.Pool
}

/*
 * Open 建立连接池并应用迁移。
 * maxConns <= 0 时使用 pgxpool 默认值；连接失败必须在装配期暴露。
 */
func Open(ctx context.Context, url string, maxConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(strings.TrimSpace(url))
	if err != nil {
		return nil, fmt.Errorf("解析 GN_DATABASE_URL: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("创建 PostgreSQL 连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 PostgreSQL: %w", err)
	}
	db := &DB{pool: pool}
	if err := db.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

/* Close 关闭连接池；Worker 与 API 共用同一实例，由 App 的释放钩子调用 */
func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

/* Ping 检查数据库可达性，供就绪探针使用 */
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

/* Name 返回提供者名称，用于启动日志 */
func (d *DB) Name() string { return "postgres" }

/*
 * Migrate 按文件名顺序执行内嵌 DDL。
 * 脚本全部使用 IF NOT EXISTS，可重复执行；首次启动即可得到可用 schema。
 */
func (d *DB) Migrate(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("读取迁移目录: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		payload, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("读取迁移 %s: %w", entry.Name(), err)
		}
		if _, err := d.pool.Exec(ctx, string(payload)); err != nil {
			return fmt.Errorf("执行迁移 %s: %w", entry.Name(), err)
		}
	}
	return nil
}

/*
 * WithinTransaction 在一个事务内执行 fn。
 * 嵌套调用复用外层事务，与内存实现「不支持嵌套」的口径一致。
 */
func (d *DB) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := txFrom(ctx); ok {
		return fn(ctx)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交事务: %w", err)
	}
	return nil
}

/* q 返回当前上下文可用的连接：事务内为 pgx.Tx，否则为连接池 */
func (d *DB) q(ctx context.Context) querier {
	if tx, ok := txFrom(ctx); ok {
		return tx
	}
	return d.pool
}

/* txFrom 从上下文取出事务，事务外返回 false */
func txFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

/* isUniqueViolation 识别唯一约束冲突（SQLSTATE 23505） */
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

/* isNoRows 识别「查询无结果」 */
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

/*
 * execBatch 顺序执行批处理语句。
 * 任一语句失败即返回错误，调用方事务随之中止，保证批量写入全成或全败。
 */
func (d *DB) execBatch(ctx context.Context, batch *pgx.Batch) error {
	results := d.q(ctx).SendBatch(ctx, batch)
	defer results.Close()
	for index := 0; index < batch.Len(); index++ {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	return results.Close()
}

/* mapNotFound 把 pgx.ErrNoRows 收敛为业务错误；其余错误包装后返回 */
func mapNotFound(err error, code errs.Code, message string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.New(code, message)
	}
	return fmt.Errorf("%s: %w", message, err)
}

/* nilToEmpty 把可空字符串转为空串，供 NOT NULL DEFAULT '' 的列使用 */
func nilToEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

/* emptyToNil 把空串还原为 nil，用于可空列 */
func emptyToNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
