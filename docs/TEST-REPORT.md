# 拾页笔记 · 服务端交付验证报告

## 0. 文档信息

| 项目 | 内容 |
|---|---|
| 报告日期 | 2026-11-01 |
| 被测对象 | 服务端 `cmd/api` 与本仓库全部包（Go 1.25.2 on Windows） |
| 真实依赖环境 | WSL2 FedoraLinux-44，Go 1.26.10 linux/amd64；Podman `--network host` |
| 真实依赖版本 | PostgreSQL **16.15**（`postgres:16-alpine`）、Redis **7.4.12**（`redis:7-alpine`） |
| 交付范围 | 第一轮：P0（6 组）+ P1（3 组）+ Nit（12 项），含 `0002` 迁移与真实依赖集成测试；第二轮：Important 2 项 + Nit 9 项 + Pre-existing 1 项，含分享页路由与 `GOMEMLIMIT` 收口；第三轮：Nit 3 项（缓存指令、单实例约束说明、报告口径） |
| 结论 | `gofmt`/`go vet`/`go build` 全部通过；`go test ./...` 全部包通过（329 个测试函数 / 78 个测试文件，0 失败）；真实 PostgreSQL + Redis 集成测试 **15/15 通过**；三轮共 18 项审查意见与 2 项遗留问题已全部闭环 |

本报告只保留最新一次实跑结果，替换修复前的「E2E + 压测」报告。历史数据产生于修复前的代码，认证、限流、事务边界与任务状态机均已变更，因此不再作为交付依据；真实依赖 E2E 与压测需在合并前按第 5 节重跑。

---

## 1. 交付范围与修复内容

| 组 | 修复内容 | 主要落点 |
|---|---|---|
| P0 数据一致性 | 笔记 revision CAS；目录 advisory lock + 路径 CAS；`0002` 新增并发索引 | `infrastructure/postgres`、`migrations/0002_*` |
| P0 回收站 | 彻底删除目录级联（笔记/页面/版本/任务/分享/容量）；目录删除失效分享 | `usecase/folder`、`usecase/trash`、postgres 仓储 |
| P0 后台清理 | `purge_at` 到期清理定时任务 | `bootstrap` 后台任务、`ListExpiredPurge` |
| P0 认证 | Refresh Token 条件更新 + 并发单胜者 | `usecase/auth`、postgres `GetForUpdate` |
| P0 安全 | 全局/登录/写/上传限流接入路由；`SetTrustedProxies` 关闭伪造 IP | `transport/http/router.go`、`middleware.go` |
| P0 转换任务 | 任务状态 CAS；用户级串行唯一约束；队列 BLMOVE + 启动恢复 | `usecase/conversion`、redis `ConversionQueue` |
| P1 认证口径 | 登录/发码统一账号枚举口径；先验码后 Argon2id | `usecase/auth` |
| P1 数据正确性 | APPEND 重试按增量结算；upload READY→USED；幂等 SETNX 占位；注销级联清理 | `usecase/note`、`usecase/upload`、`usecase/auth`、`redis/idempotency` |
| P1 测试 | 真实 PostgreSQL/Redis 集成测试；跨账号越权用例 | `infrastructure/postgres/integration_test.go`、`redis/integration_test.go`、`transport/http/*_test.go` |
| Nit 1–9、11、12 | 仓储时钟注入、块注释、`%w` 错误链、`/readyz` 固定文案、HMAC 密钥强校验、分享 token 日志脱敏、任务查询带 userID、配置文档对齐、compose 强口令 + 回环端口 + HTTP 超时/安全头、自动保存节流与进度推进、导出过滤回收站目录 | 见各包 |
| Nit 10 | Fake 时钟注入（`clock.Clock`）；邮件日志默认脱敏，`GN_FAKE_MAIL_REVEAL_CODE` 显式开启明文 | `objectstore/fake.go`、`mail/fake.go` |
| 第二轮 Important 1 | 停机取消时把在途任务回退 QUEUED 且不退款，由启动恢复/补偿协程重投；Worker 注释同步 | `usecase/conversion/runner.go`、`domain/entity/conversion_job.go`、`bootstrap/worker.go` |
| 第二轮 Important 2 | 转换结算改用 `UpdateWithRevision`（revision CAS）；冲突返回 `NOTE_MODIFIED`，不覆盖用户编辑 | `usecase/conversion/runner.go` |
| 第二轮 Nit 3–12 | 配置/包注释对齐、staging 拒绝 fake、`maskEmail` 按 rune、`.gitignore` 补 `/coverage`、限流取整复用、目录导出同级重名去重、`worker_test` 去掉负向时间断言、测试报告口径修正 | 见各包 |
| 第二轮 Pre-existing 13 | `conversion_jobs` 状态更新补 `AND user_id` 约束（G-08） | `infrastructure/postgres/media.go` |
| 遗留收口 | 实现 `GET /s/{token}` 服务端渲染分享页；`Dockerfile`/`compose.yaml` 补 `GOMEMLIMIT=300MiB` | `transport/http`、`Dockerfile`、`compose.yaml` |
| 第三轮 Nit 1–3 | 公开分享 JSON/HTML 与 410 响应加 `Cache-Control: no-store`；`ReleaseRunning`/`RecoverPending` 标注仅单实例启动可调；报告耗时按最近一次实跑刷新 | `share_handlers.go`、`media.go`、`jobs.go` |

---

## 2. 静态检查与单元测试

| 指标 | 结果 |
|---|---|
| `gofmt -l cmd internal` | 无输出 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `go test ./... -count=1 -cover` | **37 个包全部 `ok`，0 失败** |
| 测试函数 / 测试文件 | **329 / 78** |
| 无测试文件的包 | 仅 `cmd/api`（`main`）与 `internal/application/port`（纯接口） |

关键包覆盖率（`go test ./... -cover`）：

| 包 | 覆盖率 | 说明 |
|---|---|---|
| `infrastructure/pdfdoc` | 93.3% | 新增：结构/字体/JPEG 过滤/分页/取消 |
| `infrastructure/mail` | 90.6% | 新增：脱敏、明文开关、写失败、provider 校验 |
| `transport/http` | 73.7% | 跨账号 404、公开分享页与 429、限流忽略伪造头 |
| `application/usecase/{note,folder,conversion,share,export,version}` | 72.5%–83.5% | 冲突副本、级联删除、任务 CAS、停机回退、结算冲突、分享失效 |
| `infrastructure/memory` | 51.4% | 测试用仓储 |
| `infrastructure/objectstore` | 14.8% | MinIO 适配器由 E2E 覆盖；Fake 时钟注入 |
| `infrastructure/{postgres,redis}` | 本地 0.0%（未设环境变量时 `t.Skip`） | 真实覆盖见第 3 节 |

审查「关键警告」中点名的 `postgres`、`redis`、`pdfdoc`、`mail` 四个包，现已全部具备测试：前两者为真实依赖集成测试，后两者为本次交付新增单测。

---

## 3. 真实依赖集成测试（PostgreSQL 16.15 / Redis 7.4.12）

执行方式（WSL 内，容器以 `--network host` 运行）：

```bash
podman start gn-postgres gn-redis
cd /mnt/a/dev_pjs/GleanNote
GN_TEST_DATABASE_URL="postgres://gleannote:gleannote-dev-secret@127.0.0.1:5432/gleannote?sslmode=disable" \
GN_TEST_REDIS_URL="redis://127.0.0.1:6379/0" \
go test -count=1 -v ./internal/infrastructure/postgres ./internal/infrastructure/redis
```

结果：**15/15 PASS**，`postgres ok 3.585s`、`redis ok 0.047s`。

| 包 | 用例 | 验证的修复点 |
|---|---|---|
| postgres | `TestMigrateCreatesConcurrencyIndexes` | `0002` 迁移：任务串行唯一索引、`purge_at` 索引、上传状态索引 |
| postgres | `TestNoteRevisionCAS` / `TestNoteRevisionCASConcurrent` | 笔记 revision 条件更新；并发下仅一个写者成功 |
| postgres | `TestFolderLockTreeRequiresTransaction` / `TestFolderLockSerializesWriters` | 目录 advisory lock 只在事务内生效；并发改名/移动串行化 |
| postgres | `TestRefreshTokenRotationSingleWinner` | 刷新令牌原子轮换，并发仅一个请求成功 |
| postgres | `TestConversionJobUserSerial` | 同一用户同时最多一个运行中任务 |
| postgres | `TestUploadConsumeOnce` | 上传会话 READY→USED 只能消费一次，跨账号消费被拒绝 |
| postgres | `TestListExpiredPurge` | 到期回收站数据可被清理任务扫描 |
| postgres | `TestAccountPurgerRemovesAllTables` | 注销账号级联清理全部业务表 |
| redis | `TestRateLimiterFixedWindow` | 固定窗口限流计数与窗口边界 |
| redis | `TestFailureGuardLocksAndClears` | 登录失败锁定与解除 |
| redis | `TestIdempotencyReserveRelease` | 幂等键 SETNX 占位与释放 |
| redis | `TestAccessTokenDenylist` | 访问令牌 `jti` 黑名单 |
| redis | `TestConversionQueueReliableDelivery` | 队列 BLMOVE 至少一次投递与崩溃后恢复 |

另有 HTTP/用例层不依赖外部服务的越权与边界用例，随 `go test ./...` 一起执行，例如：

| 用例 | 验证的修复点 |
|---|---|
| `transport/http/ratelimit_test.go::TestPublicShareRateLimitByIP` | 公开分享按 IP 限流 60 次/分钟，超限 429 |
| `transport/http/ratelimit_test.go::TestGlobalRateLimitIgnoresForwardedHeaders` | `SetTrustedProxies` 后 `X-Forwarded-For` 不能绕过限流 |
| `transport/http/{note,folder}_api_test.go` 的跨账号断言 | 跨账号读取统一 404 |
| `usecase/version::TestVersionCrossAccountNotFound`、`usecase/share::TestShareCrossAccountNotFound`、`usecase/tag::TestTagCrossAccountNotFound` | 版本/分享/标签跨账号一律按不存在处理 |
| `bootstrap/worker_test.go` | 开发态邮件日志不含完整邮箱与明文验证码（G-10） |
| `transport/http/router_test.go::TestReadyzReportsFailure` | 就绪探针失败项只给出固定文案 `unavailable` |
| `usecase/conversion::TestCanceledContextRequeuesInFlightJob` | 停机取消时任务回落 `QUEUED` 且不退款，由恢复逻辑重投 |
| `usecase/conversion::TestSettlementConflictMarksNoteModified` | 结算 revision 冲突返回 `NOTE_MODIFIED`，不覆盖用户编辑 |
| `transport/http/version_share_api_test.go` 的分享页断言 | `GET /s/{token}` 返回 `text/html`、`no-store`、不含脚本；关闭后 `410` |

---

## 4. 环境信息

| 依赖 | 版本 | 说明 |
|---|---|---|
| Windows Go | 1.25.2 | 单测、`go vet`、`gofmt` |
| WSL Go | 1.26.10 linux/amd64 | 集成测试交叉执行环境 |
| Podman | rootful，`--network host` | `gn-postgres`、`gn-redis`、`gn-minio` |
| PostgreSQL | 16.15（Alpine） | 端口 5432，库/用户/口令均为 `gleannote` 本地值 |
| Redis | 7.4.12 | 端口 6379，`AOF` 关闭（测试用临时键） |

集成测试通过环境变量开关：`GN_TEST_DATABASE_URL`、`GN_TEST_REDIS_URL` 未设置时自动 `t.Skip`，因此普通 `go test ./...` 不依赖容器。

---

## 5. 局限与合并前待办

1. 本次交付未重跑真实依赖的 HTTP E2E 与性能压测。历史基线（132 例 E2E）产生于修复前代码，而修复改动了认证链路、限流挂载点、事务边界与任务状态机，旧数据不再适用。合并前需在联调环境重新执行 E2E，并重新采样质量目标（读 p95 ≤ 300ms、写 p95 ≤ 500ms、100 RPS）。
2. 真实 DeepSeek 识图仅有历史基线（2 页 24s、追加 1 页 2s、并发 6/6），同样产生于修复前代码；本次交付未消耗外部调用额度，转换链路的正确性由 Fake 提供者 + 用例测试覆盖。
3. 真实邮件供应商仍未接入，邮件链路仅 `fake` 适配器（本次交付已补单测与脱敏）。
4. MinIO 适配器无独立单测，由真实依赖 E2E 用例覆盖；对象存储 Fake 已改为注入时钟。
5. `internal/config` 覆盖率 25.3%，主要为既有配置项缺少用例；本次交付新增的 `GN_FAKE_MAIL_REVEAL_CODE` 解析由启动路径间接覆盖。

---

## 6. 结论

三轮审查意见已全部闭环：第一轮 P0/P1/Nit 修复落地，第二轮 2 项 Important、9 项 Nit 与 1 项 Pre-existing 已修复，第三轮 3 项 Nit（公开端点缓存指令、单实例回收约束说明、报告口径）已修复，`/s/{token}` 分享页与 `GOMEMLIMIT` 两项遗留问题也已收口。当前代码在 Windows 上静态检查、构建与 329 个测试函数全部通过；在真实 PostgreSQL 16.15 与 Redis 7.4.12 上，覆盖并发 CAS、目录串行、令牌轮换、任务串行、上传单次消费、幂等占位与队列可靠投递的 15 个集成用例全部通过。审查中指出的「真实适配器无测试」问题已闭环；E2E 与压测需按第 5 节在合并前重跑。
