# 拾页笔记 GleanNote 服务端

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

图片 → Markdown 笔记应用的服务端（仅后端）。技术栈：Go 1.25 + Gin；部署形态为单二进制模块化单体，主库 PostgreSQL 16，队列 Redis（BLMOVE 可靠队列），对象存储 MinIO（S3 兼容）。

## 功能

- **账号与认证**：邮箱验证码注册/登录、Ed25519 访问令牌与刷新令牌轮换、重置密码、注销账号、额度查询。
- **目录**：最多 5 层嵌套、面包屑、同级重名校验、改名与移动的乐观锁和环检测、批次软删除与恢复。
- **笔记**：`DRAFT → NORMAL → DELETED` 状态机、按 `revision` 的乐观锁与冲突副本、键集分页、全文搜索、30 天回收站。
- **图片与转换**：预签名直传与校验、缩略图生成、`createFromPages` 与追加页面、异步转换任务（取消/重试/失败返还）、Markdown 正文与标题生成。
- **版本与分享**：五类版本快照、自动保存节流与恢复；分享链接、公开 JSON 端点与 `GET /s/{token}` 只读分享页。
- **标签与同步**：账号级标签、基于游标的增量同步。
- **导出与导入**：Markdown ZIP / PDF 导出、按图或合并的批量导入。
- **运行形态**：单二进制模块化单体，`GN_MODE=api|worker|all` 可拆分 HTTP 与转换 Worker，提供 `/healthz` 与 `/readyz` 探针。

## 快速开始

需要 Docker（含 Compose v2）。仓库根目录的 `compose.yaml` 会一并拉起 API/Worker、PostgreSQL 16、Redis 7 与 MinIO：

```bash
cp .env.example .env
docker compose up -d --build
```

默认入口 `http://127.0.0.1:8080`，探针为 `/healthz` 与 `/readyz`。全部配置项、默认值与取值约束见 [.env.example](.env.example)，修改 `.env` 后重新执行 `docker compose up -d` 即生效。

## 文档

| 文档 | 内容 |
|---|---|
| [API.md](docs/API.md) / [openapi.yaml](docs/openapi.yaml) | 接口契约、错误码与字段定义 |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | 分层、数据模型、横切约束与配置项附录 |
| [TEST-REPORT.md](docs/TEST-REPORT.md) | 验证结果与覆盖率 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 分支、提交、自检与审查流程 |

## 测试

```bash
gofmt -l cmd internal
go vet ./...
go build ./...
go test ./... -count=1
```

集成测试需要真实 PostgreSQL 16 与 Redis 7，通过环境变量开启；未设置时对应用例自动跳过：

```bash
GN_TEST_DATABASE_URL="postgres://gleannote:gleannote-dev-secret@127.0.0.1:5432/gleannote?sslmode=disable" \
GN_TEST_REDIS_URL="redis://127.0.0.1:6379/0" \
go test -count=1 ./internal/infrastructure/postgres ./internal/infrastructure/redis
```

## 目录结构

```text
cmd/api/                     # 单二进制入口：加载配置 → 装配 → 监听 → 优雅退出
internal/domain/             # 领域层：实体、值对象、错误码与跨实体规则（不依赖框架）
internal/application/port/   # 端口接口：仓储、事务、令牌、哈希、限流、付费能力等
internal/application/usecase/# 用例编排：auth 账号、folder 目录树、note 笔记、upload 上传、conversion 转换任务、version 版本、share 分享
internal/config/             # 环境变量加载与校验
internal/infrastructure/     # 适配器：vision / mail / objectstore / imageprocessor / token / password / codehash / memory
internal/platform/           # 无业务地基：clock / idgen / bizdate / random / requestid / pagetoken / problem
internal/transport/http/     # HTTP 路由、中间件、处理器与错误渲染
internal/bootstrap/          # 装配根：选择 provider、构造用例、转换 Worker、生成路由、释放钩子
docs/                        # PRD、架构、API、OpenAPI、规范
```

## 许可

本项目采用 [Apache License 2.0](LICENSE)。贡献代码前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。
