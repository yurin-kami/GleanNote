/*
 * 0003 转换任务的「未识别页面」明细（PostgreSQL 16）。
 *
 * 约定：本脚本只做结构变更，可重复执行。
 * 背景：识图结果只把占位文案写进正文，客户端要靠正则解析才能知道哪几页失败；
 * 这里把页码显式落库，GET conversionJob 直接返回，正文解析退化为兜底。
 * 参考：docs/API.md#551-任务对象、docs/ARCHITECTURE.md#57-转换任务状态机
 */

ALTER TABLE conversion_jobs
    ADD COLUMN IF NOT EXISTS unreadable_pages jsonb NOT NULL DEFAULT '[]'::jsonb;
