# 贡献指南（CONTRIBUTING）

## 0. 文档信息

| 项目 | 内容 |
|---|---|
| 文档编号 | GN-CONTRIB-001 |
| 版本 | v1.0 |
| 状态 | 已评审基线 |
| 更新日期 | 2026-11-01 |
| 适用范围 | 本仓库的代码、文档与配置变更 |
| 上级约束 | [CONVENTIONS.md](docs/CONVENTIONS.md)（注释、分层、Git 与文档规范）、[ARCHITECTURE.md](docs/ARCHITECTURE.md) 第 8 章横切约束 |

本文只规定「如何提交与合并改动」；编码细节以 [CONVENTIONS.md](docs/CONVENTIONS.md) 为准，冲突时以上级约束文档优先。

## 1. 适用范围与约定

- 本仓库仅包含服务端；客户端实现不得进入本仓库。
- 业务规则以 [PRD](docs/PRD-拾页笔记-GleanNote.md) 为准，接口字段以 [openapi.yaml](docs/openapi.yaml) 与 [API.md](docs/API.md) 为准。
- 术语与用词固定为「必须 / 应当 / 可以 / 不得」，语义按 RFC 2119（CONVENTIONS D-04）。

## 2. 开发环境

| 依赖 | 版本 | 用途 |
|---|---|---|
| Go | 1.25 及以上 | 构建、静态检查与单元测试 |
| PostgreSQL | 16 | 集成测试与运行态 |
| Redis | 7 | 限流、幂等、令牌黑名单与转换队列 |
| MinIO | 任意较新版本 | 对象存储（S3 兼容） |

本地一键拉起真实依赖：`docker compose up -d`（或使用 `compose.yaml` 中等价的 Podman 命令）。集成测试通过 `GN_TEST_DATABASE_URL`、`GN_TEST_REDIS_URL` 开启，未设置时自动 `t.Skip`。

## 3. 分支与提交

### 3.1 分支

| 分支 | 用途 | 生命周期 |
|---|---|---|
| `main` | 可发布基线，禁止直接推送 | 长期 |
| `feat/<topic>` | 功能开发 | 合并后删除 |
| `fix/<topic>` | 缺陷修复 | 合并后删除 |
| `docs/<topic>` | 文档 | 合并后删除 |

### 3.2 提交信息

遵循 Conventional Commits 1.0.0，格式为 `<type>(<scope>): <subject>`；`type` 取 `feat`/`fix`/`docs`/`refactor`/`perf`/`test`/`build`/`ci`/`chore`，`scope` 使用包名或模块名（如 `auth`、`conversion`）。标题不超过 72 字符、中文陈述句、不以句号结尾；破坏性变更在 footer 写 `BREAKING CHANGE: 说明`。

### 3.3 提交粒度

一次提交只做一件事；文档变更与代码变更应当分开提交；不得提交 `.env`、密钥、构建产物与本地数据目录（见 `.gitignore`）。

## 4. 提交前自检

提交前必须在本机跑通以下检查，并以实际输出（而非口头声明）作为证据：

```bash
gofmt -l cmd internal
go vet ./...
go build ./...
go test ./... -count=1
```

改动涉及 Postgres/Redis 适配器、事务边界或队列语义时，还必须跑真实依赖集成测试：

```bash
GN_TEST_DATABASE_URL="postgres://gleannote:gleannote-dev-secret@127.0.0.1:5432/gleannote?sslmode=disable" \
GN_TEST_REDIS_URL="redis://127.0.0.1:6379/0" \
go test -count=1 -v ./internal/infrastructure/postgres ./internal/infrastructure/redis
```

新增行为必须有单元测试或集成测试覆盖；缺陷修复应当同时补上能复现该缺陷的用例。

## 5. 变更对文档与契约的要求

| 改动类型 | 必须同步更新 |
|---|---|
| 新增/修改端点、字段、错误码、枚举 | `docs/API.md` 与 `docs/openapi.yaml`，并更新两者修订记录 |
| 架构决策、分层规则、部署形态 | `docs/ARCHITECTURE.md`（含 ADR 与修订记录） |
| 环境变量、默认值、启动约束 | `README.md` 环境变量表与 `.env.example` |
| 依赖真实行为的验证结果 | `docs/TEST-REPORT.md`（只保留最新一次实跑结果） |
| 数据库结构 | `internal/infrastructure/postgres/migrations/` 新增迁移文件，**不得**修改已发布的迁移 |

文档必须保留「文档信息」表与修订记录（CONVENTIONS D-01、D-02），版本号按 `v主.次`。

## 6. 审查流程与分级

审查流程对齐全局 `AGENTS.md` 的代码审查规范（来源：Anthropic《How the Claude Code team does code review》，2026-10-15）。

### 6.1 分层校验（顺序执行）

1. **提交前自检**：跑通第 4 节命令并展示输出。
2. **确定性检查**：`gofmt`、`go vet`、`go build`、测试必须全部通过。
3. **AI/同行审查**：只审本次 diff 边界内的改动及其调用点，不评论未改动代码；对照变更意图判断「是否解决问题、是否契合系统、是否破坏别处」；缺陷修复必须查根因。
4. **安全审查（独立一轮）**：存在未解决的安全问题不得合并；重点检查鉴权、越权、注入、密钥与日志脱敏。
5. **人工批准**：按 6.4 的风险分级执行。
6. **合并**：在 `main` 上重跑全部检查后再合并；合并后发现异常按回滚优先处理。

### 6.2 轮次与噪声预算

- 首轮评审应当一次把问题出全，禁止逐轮「挤牙膏」式追加新问题。
- 第 2 轮起只审新增 diff，不复述历史问题。
- 超过 3 轮仍未收敛时，应当重新考虑方案，而不是继续循环。

### 6.3 问题分级

审查意见统一分为 `Important`、`Nit`、`Pre-existing` 三档，每条必须给出 `文件:行号` 证据；每条意见要么修复，要么在评审线程中说明理由后关闭。转述结论时应当以计数开头（如「Important 0 · Nit 3 · Pre-existing 0」）。

### 6.4 风险分级与批准

| 档位 | 示例 | 人工批准 |
|---|---|---|
| 低档 | 文档、文案、内部工具改动 | 自动化审查与确定性检查通过即可合并 |
| 中档 | 普通业务代码、缓存、限流、提示词等敏感区域 | 1 人 |
| 高档 / 安全 | 认证授权、信任边界、密钥与 CI 工作流文件 | 1 人（安全审查仅由 1 人完成） |

一次批准只覆盖一个 commit；新增 commit 需要重新批准。

## 7. 安全与许可

- 安全漏洞不得通过公开 Issue 报告，应当使用 GitHub Security Advisory 的私密报告通道。
- 不得在代码、日志、测试数据与文档中写入真实密钥或个人敏感信息。
- 本项目采用 [Apache License 2.0](LICENSE)。提交 Pull Request 即表示你同意按该许可授权你的贡献，并确认有权做出该授权。

## 8. 修订记录

| 版本 | 日期 | 修订人 | 说明 |
|---|---|---|---|
| v1.0 | 2026-11-01 | 服务端研发组 | 首版：开发环境、分支与提交、自检命令、文档同步要求、审查流程与分级、许可约定 |
