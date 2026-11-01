/*
 * 0002 并发控制与到期清理所需的结构补充（PostgreSQL 16）。
 *
 * 约定：本脚本只做结构变更，可重复执行。
 * 进程重启后遗留的 RUNNING 任务由 Worker 启动恢复（RecoverPending）回落为 QUEUED，
 * 不在迁移里重置，避免仅提供 HTTP 的实例误伤正在运行的 Worker。
 * 参考：docs/ARCHITECTURE.md#57-转换任务状态机、docs/ARCHITECTURE.md#64-回收站恢复与到期清理
 */

/* 同一账号同一时刻只允许一个 RUNNING 转换任务，从数据库层面保证用户串行 */
CREATE UNIQUE INDEX IF NOT EXISTS ux_conversion_jobs_running
    ON conversion_jobs (user_id) WHERE state = 'RUNNING';

/* 到期清理按 purge_at 扫描，避免全表扫描 */
CREATE INDEX IF NOT EXISTS ix_notes_purge_at
    ON notes (purge_at) WHERE purge_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_folders_purge_at
    ON folders (purge_at) WHERE purge_at IS NOT NULL;

/* 上传会话按状态过滤，支撑「一次性消费」与过期清理 */
CREATE INDEX IF NOT EXISTS ix_uploads_user_status ON uploads (user_id, status);
