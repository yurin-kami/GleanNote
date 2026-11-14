# 拾页笔记 · 服务端性能采样报告

## 0. 文档信息

| 项目 | 内容 |
|---|---|
| 文档编号 | GN-PERF-001 |
| 版本 | v1.0 |
| 状态 | 已实测（单机开发环境基线） |
| 更新日期 | 2026-11-14 |
| 适用范围 | 服务端 `cmd/api` 的 HTTP 读写路径容量基线 |
| 上级约束 | [ARCHITECTURE.md](ARCHITECTURE.md) 第 8.7 节限流口径、[CONVENTIONS.md](CONVENTIONS.md) 第 6 章文档规范 |

本报告只记录一次可复现的实跑结果；环境、配额与脚本均在文中给出，换环境需重跑而非套用数字。

## 1. 目的与范围

回答两个问题：

1. 放开限流后，读写接口在目标速率下的延迟是否满足质量目标（读 p95 ≤ 300 ms、写 p95 ≤ 500 ms、100 RPS）；
2. 单实例在 2 vCPU 环境下能撑到多少，以及最先撞到的瓶颈是什么。

不在范围内：图片上传/识图转换链路（依赖外部供应商）、TLS 与前置代理、多实例横向扩容。

## 2. 环境与方法

| 项 | 取值 |
|---|---|
| 运行位置 | WSL2 Fedora 44，Docker 29.6.2 + Compose v5.3.1 |
| 资源 | 2 vCPU / 4 GB（`wslconfig` 限制） |
| 服务形态 | 单实例 `GN_MODE=api`（不含 Worker），与生产同一镜像 `gleannote:local` |
| 依赖 | PostgreSQL 16、Redis 7、MinIO 均真实容器 |
| 数据隔离 | 独立库 `gleannote_perf` + Redis DB 15，压测后整体删除 |
| 限流 | 通过 `GN_RATE_*` 放开到 1000000（生产默认 600/120/10/100/60） |
| 客户端 | Python `http.client`，每 worker 复用 keep-alive 连接，按目标速率匀速发压 |
| 样本 | 每档 15 s，记录 p50/p95/p99 与状态码分布 |
| 数据规模 | 从空库起步；写入档累计新增 304 + 760 + 1520 + 3000 条笔记 |

## 3. 实测结果

读取 `GET /v1/notes?folderId=...`（带 Bearer 鉴权，返回分页集合）：

| 目标速率 | 实际 | 请求数 | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|---|
| 50 rps | 50.0 | 750 | 6.2 ms | 9.3 ms | 21.1 ms | 0 |
| 100 rps | 99.8 | 1500 | 3.1 ms | 5.6 ms | 8.0 ms | 0 |
| 200 rps | 199.5 | 3000 | 5.7 ms | 11.0 ms | 16.3 ms | 0 |
| 300 rps | 298.4 | 4500 | 5.7 ms | 20.7 ms | 30.1 ms | 0 |
| 500 rps | 496.4 | 7500 | 8.9 ms | 34.9 ms | 50.8 ms | 0 |

写入 `POST /v1/notes`（带 `Idempotency-Key`，每次创建一条笔记）：

| 目标速率 | 实际 | 请求数 | p50 | p95 | p99 | 错误 |
|---|---|---|---|---|---|---|
| 20 rps | 20.0 | 304 | 13.1 ms | 29.6 ms | 62.2 ms | 0 |
| 50 rps | 49.9 | 760 | 7.3 ms | 13.0 ms | 21.2 ms | 0 |
| 100 rps | 99.3 | 1520 | 5.1 ms | 19.9 ms | 34.0 ms | 0 |
| 200 rps | 199.5 | 3000 | 110.8 ms | 139.8 ms | 154.7 ms | 0 |

## 4. 结论

1. **质量目标达成**：读在 500 rps 下 p95 34.9 ms（目标 ≤ 300 ms），写在 200 rps 下 p95 139.8 ms（目标 ≤ 500 ms），全程 0 错误。
2. **100 RPS 目标有充足余量**：读取比目标高 5 倍、写入比目标高 2 倍仍满足延迟要求。
3. **瓶颈出现在写入的 200 rps 档**：p50 从 5.1 ms 跃升到 110.8 ms，符合 2 vCPU 下数据库写入与 Argon2id 之外的写放大特征；读路径在该并发下仍平稳（500 rps p95 34.9 ms）。
4. **限流才是默认部署下的真实上限**：默认 600 次/分（读 10 rps）与 120 次/分（写 2 rps）会先于容量生效，线上如需更高吞吐，应调整 `GN_RATE_*` 而不是换硬件。

## 5. 限制与注意事项

- 单机单实例、WSL 虚拟化环境，未经过真实云主机与前置代理，绝对值仅供同机对比与回归基线。
- 未覆盖图片上传、识图转换、导出等重路径；这些路径受外部供应商与 CPU 图像处理约束，需单独采样。
- 写入样本从空库增长到约 5600 条，未做千万级数据下的索引退化测试。
- 压测时关闭了限流，**生产不得使用该配置**；放开限流仅用于测量容量。

## 6. 复现步骤

```bash
# 1. 准备独立库
docker exec gleannote-postgres-1 psql -U gleannote -d postgres -c "CREATE DATABASE gleannote_perf"

# 2. 用主实例环境派生压测实例（放开限流、切库、切 Redis DB）
BASE_ENV=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' gleannote-api-1)
# ... 过滤 GN_MODE/GN_RATE_*/GN_DATABASE_URL/GN_REDIS_URL 后追加：
#   GN_MODE=api
#   GN_DATABASE_URL=<主实例 URL 的库名换成 gleannote_perf>
#   GN_REDIS_URL=redis://redis:6379/15
#   GN_RATE_GLOBAL_PER_MIN=1000000 等
docker run -d --name gleannote-api-perf --network gleannote_default \
  --env-file /tmp/perf.env -p 18080:8080 gleannote:local

# 3. 按第 3 节的速率档发压（客户端需复用 keep-alive 连接），完成后清理：
docker rm -f gleannote-api-perf
docker exec gleannote-postgres-1 psql -U gleannote -d postgres -c "DROP DATABASE gleannote_perf"
```

## 7. 修订记录

| 版本 | 日期 | 修订人 | 说明 |
|---|---|---|---|
| v1.0 | 2026-11-14 | 服务端研发组 | 首版：放开限流后的读写容量基线，含五档读、四档写与瓶颈结论 |
