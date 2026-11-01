# 拾页笔记 GleanNote 服务端

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

图片 → Markdown 笔记应用的服务端（仅后端）。技术栈：Go 1.25 + Gin；部署形态为单二进制模块化单体，主库 PostgreSQL 16，队列 Redis（BLMOVE 可靠队列），对象存储 MinIO（S3 兼容）。

产品规则以 [PRD](docs/PRD-拾页笔记-GleanNote.md) 为准，技术方案见 [架构文档](docs/ARCHITECTURE.md)，接口契约见 [API 文档](docs/API.md) 与 [OpenAPI](docs/openapi.yaml)，编码与注释规则见 [规范](docs/CONVENTIONS.md)。

## 功能

- **账号与认证**：邮箱验证码注册/登录、Ed25519 访问令牌与刷新令牌轮换、重置密码、注销账号、额度查询。
- **目录**：最多 5 层嵌套、面包屑、同级重名校验、改名与移动的乐观锁和环检测、批次软删除与恢复。
- **笔记**：`DRAFT → NORMAL → DELETED` 状态机、按 `revision` 的乐观锁与冲突副本、键集分页、全文搜索、30 天回收站。
- **图片与转换**：预签名直传与校验、缩略图生成、`createFromPages` 与追加页面、异步转换任务（取消/重试/失败返还）、Markdown 正文与标题生成。
- **版本与分享**：五类版本快照、自动保存节流与恢复；分享链接、公开 JSON 端点与 `GET /s/{token}` 只读分享页。
- **标签与同步**：账号级标签、基于游标的增量同步。
- **导出与导入**：Markdown ZIP / PDF 导出、按图或合并的批量导入。
- **运行形态**：单二进制模块化单体，`GN_MODE=api|worker|all` 可拆分 HTTP 与转换 Worker，提供 `/healthz` 与 `/readyz` 探针。

## 环境要求

| 依赖 | 版本 | 说明 |
|---|---|---|
| Go | 1.25 及以上 | `go version` 确认 |
| Git | 任意较新版本 | 拉取与提交 |
| Docker Desktop / Podman | 任意较新版本 | 用于启动本地 MinIO、PostgreSQL 与 Redis 容器 |
| PostgreSQL | 16（运行态必选） | `GN_REPOSITORY_PROVIDER=postgres` 时需要 |
| Redis | 7（运行态必选） | `GN_CACHE_PROVIDER=redis` 时需要 |

## 启动开发服务

### 0. 一键启动（推荐）

仓库根目录提供 `compose.yaml` 与 `.env.example`，一条命令拉起 API/Worker、PostgreSQL 16、Redis 7 与 MinIO（真实依赖，非 fake）：

```bash
cp .env.example .env          # Windows PowerShell: Copy-Item .env.example .env
docker compose up -d --build  # Podman: podman compose up -d --build
```

- 入口：默认只映射到宿主回环地址 `http://127.0.0.1:<GN_HTTP_PORT|8080>`（容器内监听 `0.0.0.0:8080`），`/healthz`、`/readyz` 可直接探活；需要局域网直连时自行改 `compose.yaml` 的端口映射
- MinIO：S3 `http://127.0.0.1:9000`、控制台 `http://127.0.0.1:9001`（凭据见 `.env` 的 `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`，服务端 `GN_S3_*` 由它们派生）
- 数据卷：`postgres-data`、`redis-data`、`minio-data`；`docker compose down -v` 会一并清除
- 国内拉取 MinIO 慢时，可先按 `.env.example` 注释里的华为云镜像命令拉取并重命名为官方名称
- 服务端 `GN_MODE=all`（HTTP + 转换 Worker），视觉默认 `fake`（在 `.env` 填入 `GN_VISION_API_KEY` 并改为 `deepseek` 即真实识图），邮件仍为 `fake`；改动 `.env` 后 `docker compose up -d` 会自动重建

不想用编排时，按下面 1–3 步手动启动单个容器与服务。

### 1. 启动本地 MinIO 容器

```powershell
docker run -d --name gleannote-minio -p 9000:9000 -p 9001:9001 -e MINIO_ROOT_USER=gleannote -e MINIO_ROOT_PASSWORD=gleannote-dev-secret -v gleannote-minio-data:/data minio/minio:latest server /data --console-address ":9001"
```

- S3 接口：`http://127.0.0.1:9000`
- 控制台：`http://127.0.0.1:9001`（账号密码同上，仅本地开发使用）
- 桶由服务启动时自动创建，默认 `gleannote-private`，保持私有，访问一律走预签名地址

容器已存在时用 `docker start gleannote-minio`；确认可用：`docker ps --filter name=gleannote-minio`。

### 2. 设置对象存储环境变量

PowerShell：

```powershell
$env:GN_OBJECT_STORE_PROVIDER="minio"
$env:GN_S3_ENDPOINT="127.0.0.1:9000"
$env:GN_S3_ACCESS_KEY="gleannote"
$env:GN_S3_SECRET_KEY="gleannote-dev-secret"
$env:GN_S3_BUCKET="gleannote-private"
```

bash / zsh：

```bash
export GN_OBJECT_STORE_PROVIDER=minio
export GN_S3_ENDPOINT=127.0.0.1:9000
export GN_S3_ACCESS_KEY=gleannote
export GN_S3_SECRET_KEY=gleannote-dev-secret
export GN_S3_BUCKET=gleannote-private
```

`GN_S3_ENDPOINT` 支持 `host:port` 或 `http(s)://host:port`（显式协议优先于 `GN_S3_USE_SSL`）。`GN_S3_REGION` 默认 `us-east-1`。

### 2.1 启动 PostgreSQL 与 Redis（可选，运行态推荐）

默认 `GN_REPOSITORY_PROVIDER=memory`、`GN_CACHE_PROVIDER=memory`，无需外部依赖。希望数据落库并获得跨实例共享的限流与队列时，启动两个容器：

```powershell
docker run -d --name gleannote-postgres -p 5432:5432 -e POSTGRES_USER=gleannote -e POSTGRES_PASSWORD=gleannote-dev-secret -e POSTGRES_DB=gleannote -v gleannote-pg-data:/var/lib/postgresql/data postgres:16-alpine
docker run -d --name gleannote-redis -p 6379:6379 -v gleannote-redis-data:/data redis:7-alpine
```

```powershell
$env:GN_REPOSITORY_PROVIDER="postgres"
$env:GN_DATABASE_URL="postgres://gleannote:gleannote-dev-secret@127.0.0.1:5432/gleannote?sslmode=disable"
$env:GN_CACHE_PROVIDER="redis"
$env:GN_REDIS_URL="redis://127.0.0.1:6379/0"
```

```bash
export GN_REPOSITORY_PROVIDER=postgres
export GN_DATABASE_URL="postgres://gleannote:gleannote-dev-secret@127.0.0.1:5432/gleannote?sslmode=disable"
export GN_CACHE_PROVIDER=redis
export GN_REDIS_URL="redis://127.0.0.1:6379/0"
```

启动时服务按文件名顺序执行内嵌迁移（`internal/infrastructure/postgres/migrations/*.sql`，全部 `IF NOT EXISTS`，可重复执行），并校验 Redis 连通性；任一连接失败都会终止启动。

### 3. 启动服务

```powershell
go mod tidy
go run ./cmd/api
```

启动成功后会看到 JSON 日志：`转换 Worker 已启动`（仅 `all`/`worker` 模式）、`装配完成`（含三个 provider 名称）与 `HTTP 服务已启动`（`api`/`all` 模式）。默认监听 `0.0.0.0:8080`。

验证探针（PowerShell 中 `curl` 是别名，请使用 `curl.exe`）：

```powershell
curl.exe -s http://127.0.0.1:8080/healthz
curl.exe -s http://127.0.0.1:8080/readyz
```

预期输出：

```json
{"status":"ok"}
{"status":"ok","env":"dev","checks":{"mailer":"ok","objectStore":"ok","vision":"ok"}}
```

`/readyz` 在任一所装配能力不可用时返回 `503`，`status` 为 `unavailable`，失败项统一给出 `unavailable`（不返回底层错误文本，避免向匿名探针泄漏内部细节）。停止服务使用 `Ctrl+C`，进程会先停止接收新请求，再在 10 秒内等待在途请求结束。

### 4. 指定端口与日志级别

```powershell
$env:GN_HTTP_ADDR="127.0.0.1:9090"; $env:GN_LOG_LEVEL="debug"; go run ./cmd/api
```

```bash
GN_HTTP_ADDR=127.0.0.1:9090 GN_LOG_LEVEL=debug go run ./cmd/api
```

### 5. 试用认证接口

`GN_MAILER_PROVIDER=fake` 时验证码不会真的发送，而是以单行 JSON 写入服务标准输出。日志默认脱敏，形如 `{"level":"info","event":"mail.fake.send","to":"d***@example.com","subject":"...","body":"您的验证码是 ***..."}`；本地手工联调需要读到真实验证码时，先设置 `GN_FAKE_MAIL_REVEAL_CODE=true`（仅开发环境使用，默认关闭）。先申请验证码：

```powershell
curl.exe -s -i -X POST http://127.0.0.1:8080/v1/auth:sendCode `
  -H "Content-Type: application/json" `
  -d '{"email":"demo@example.com","purpose":"REGISTER"}'
```

成功返回 `204`。注册需要 `Idempotency-Key`（8–64 位可见 ASCII），重放同一键与同一请求体会原样返回首次结果：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/auth:register `
  -H "Content-Type: application/json" `
  -H "Idempotency-Key: demo-register-0001" `
  -d '{"email":"demo@example.com","code":"123456","password":"Passw0rd!","nickname":"演示用户","agreements":{"userAgreement":true,"privacyPolicy":true}}'
```

返回 `201` 与 `{"user":{...},"accessToken":"...","refreshToken":"...","expiresIn":900,"tokenType":"Bearer"}`。带令牌访问当前用户：

```powershell
curl.exe -s http://127.0.0.1:8080/v1/users/me -H "Authorization: Bearer <accessToken>"
```

错误响应统一为 `application/problem+json`：`{"type":"...","title":"...","status":400,"code":"VALIDATION_FAILED","detail":"...","instance":"/v1/auth:register#req_...","requestId":"req_..."}`。常见码：`AUTH_TOKEN_INVALID`、`AUTH_TOKEN_EXPIRED`、`AUTH_REFRESH_REUSED`、`IDEMPOTENCY_KEY_REUSED`、`RATE_LIMITED`。可用的环境变量见下表。

### 6. 试用目录接口

注册成功后会自动创建第 1 层目录「我的笔记」。列出根级目录：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/folders" -H "Authorization: Bearer <accessToken>"
```

在根目录下新建子目录（同样需要 `Idempotency-Key`）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/folders `
  -H "Content-Type: application/json" `
  -H "Idempotency-Key: demo-folder-0001" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"parentId":"<rootFolderId>","name":"数学"}'
```

移动、恢复与彻底删除走 AIP-136 冒号方法：`POST /v1/folders/{folderId}:move`（`{"targetParentId":null}` 表示移到根级）、`:undelete`、`:expunge`。同级重名返回 `409 NAME_DUPLICATED`，层级超限返回 `409 FOLDER_DEPTH_EXCEEDED`，移动到子孙返回 `409 FOLDER_CYCLE`，改名修订号过期返回 `412 REVISION_MISMATCH`。

### 7. 试用笔记接口

在目录内新建笔记（`Idempotency-Key` 必填）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/notes `
  -H "Content-Type: application/json" `
  -H "Idempotency-Key: demo-note-0001" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"folderId":"<folderId>","title":"矩阵的秩","content":"# 矩阵的秩\n..."}'
```

列出目录内笔记（`folderId` 必填；默认按更新时间倒序、排除回收站）：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/notes?folderId=<folderId>&pageSize=20" -H "Authorization: Bearer <accessToken>"
```

保存使用 `PATCH /v1/notes/{noteId}`，请求体带 `revision` 与 `saveSource`（`MANUAL_SAVE`/`AUTO_SAVE`）。修订号落后时返回 `200` 且附 `conflictCopyId`，原正文保留为一份标题带「（冲突副本 时间）」的笔记；仅在正文超过 20 万字符时返回 `413 NOTE_TOO_LARGE`。移动、恢复与彻底删除分别为 `POST /v1/notes/{noteId}:move`、`:undelete`、`:expunge`。

### 8. 试用图片上传与转换

先申请上传会话（1–20 张，仅 `image/jpeg`、`image/png`、`image/webp`，单张 ≤10MB）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/uploads `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"items":[{"fileName":"p1.png","contentType":"image/png","bytes":<字节数>,"sha256":"<SHA256>"}]}'
```

返回 `201`，`items[0]` 含 `uploadId`、`objectKey`、`putUrl`、`headers` 与 `expiresAt`（30 分钟）。把文件直传到 `putUrl`（必须带上 `headers` 中的 `Content-Type`），然后确认：

```powershell
curl.exe -s -X POST "http://127.0.0.1:8080/v1/uploads/<uploadId>:complete" `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"sha256":"<SHA256>"}'
```

服务端会校验对象存在、字节数与摘要，并解码校验尺寸后生成缩略图；摘要不符返回 `409 UPLOAD_HASH_MISMATCH`。随后创建笔记（`Idempotency-Key` 必填）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/notes:createFromPages `
  -H "Content-Type: application/json" `
  -H "Idempotency-Key: demo-pages-0001" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"folderId":"<folderId>","uploadIds":["<uploadId>"],"title":"可选标题"}'
```

返回 `201`：笔记为 `DRAFT` 且已回填 `pageCount`/`thumbnailUrl`，`conversionJob.state` 为 `QUEUED`，`quota` 给出本次扣减与剩余。`GN_MODE=all|worker` 时进程内 Worker 会自动消费：用 `GET /v1/notes/{noteId}/conversionJob` 轮询，成功后状态为 `SUCCEEDED`、笔记转 `NORMAL` 且正文为 Markdown；失败码含 `EMPTY_RESULT`、`PROVIDER_UNAVAILABLE`、`TIMEOUT` 等，失败/取消/超时会返还额度。也可用 `POST /v1/notes/{noteId}/conversionJob:cancel` 取消（仅 `QUEUED`/`RUNNING`）或 `:retry` 重试（新建任务并重新扣减额度）。页面地址通过 `GET /v1/notes/{noteId}/pages` 与 `GET /v1/notes/{noteId}/pages/{pageId}` 获取，均为 10 分钟预签名。

### 9. 试用标签、搜索与回收站

新建标签（`Idempotency-Key` 必填；账号内重名返回 `409 NAME_DUPLICATED`）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/tags `
  -H "Content-Type: application/json" `
  -H "Idempotency-Key: demo-tag-0001" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"name":"考研"}'
```

列出标签（默认 `noteCount desc`，也支持 `orderBy=name asc` 与 `pageSize`/`pageToken`）：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/tags?pageSize=20" -H "Authorization: Bearer <accessToken>"
```

整体覆盖一篇笔记的标签（单篇 ≤10，超出返回 `409 TAG_LIMIT_EXCEEDED`；标签不存在返回 `404`）：

```powershell
curl.exe -s -X PUT http://127.0.0.1:8080/v1/notes/<noteId>/tags `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer <accessToken>" `
  -d '{"tagIds":["<tagId>"]}'
```

搜索（`query` 1–100 字符，`folderId` 含子树，`tagIds` 可重复且语义为同时包含）：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/notes:search?query=%E7%A7%A9&folderId=<folderId>" -H "Authorization: Bearer <accessToken>"
```

结果按「标题命中优先、同级更新时间倒序」排列，`snippet` 只保留 `<em>` 一种标记，其余正文标记已转义。回收站按删除批次聚合目录与笔记：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/trash?pageSize=20" -H "Authorization: Bearer <accessToken>"
```

每条给出 `batchId`、`itemType`（`FOLDER`/`NOTE`）、`itemId`、`originalPath`、`folderCount`、`noteCount`、`deletedAt`、`purgeAt` 与 `remainingDays`；恢复与彻底删除仍按类型调用对应资源的 `:undelete` / `:expunge`。

### 10. 试用版本与分享

保存两次或转换完成后可查看版本列表（按时间倒序，AIP-158 分页）：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/notes/<noteId>/versions?pageSize=20" -H "Authorization: Bearer <accessToken>"
```

单条版本详情与恢复（恢复只替换正文，并生成一份 `RESTORE` 新版本；`Idempotency-Key` 必填）：

```powershell
curl.exe -s "http://127.0.0.1:8080/v1/notes/<noteId>/versions/<versionId>" -H "Authorization: Bearer <accessToken>"

curl.exe -s -X POST "http://127.0.0.1:8080/v1/notes/<noteId>/versions/<versionId>:restore" `
  -H "Idempotency-Key: demo-version-restore-0001" `
  -H "Authorization: Bearer <accessToken>"
```

开启分享（`Idempotency-Key` 必填；仅 `NORMAL` 状态可开启，草稿返回 `409 SHARE_NOTE_NOT_READY`）：

```powershell
curl.exe -s -X POST http://127.0.0.1:8080/v1/notes/<noteId>/shareLink `
  -H "Idempotency-Key: demo-share-0001" `
  -H "Authorization: Bearer <accessToken>"
```

返回 `201` 与 `{"enabled":true,"url":"https://gleannote.example/s/<token>","createdAt":"...","updatedAt":"..."}`。查询状态用 `GET /v1/notes/{noteId}/shareLink`（未开启时只有 `enabled:false`），关闭用 `DELETE`（返回 `204`，链接立即失效）。匿名读取无需任何凭证：

```powershell
curl.exe -s http://127.0.0.1:8080/v1/public/shares/<token>
```

返回标题、正文、页码与 10 分钟预签名原图地址。公开地址由 `GN_PUBLIC_BASE_URL` 决定；同地址的 `GET /s/{token}` 返回服务端渲染的只读 HTML 页（正文纯文本转义、CSP 禁脚本），两者均带 `Cache-Control: no-store`，撤销即时生效；匿名端点按 IP 限流 60 次/分钟（超出 `429 RATE_LIMITED`），链接关闭、笔记或所属目录已删除时统一返回 `410 SHARE_LINK_INACTIVE`。

## 环境变量

完整清单与默认值见 [ARCHITECTURE.md 附录 A](docs/ARCHITECTURE.md#附录-a配置项清单环境变量)，样例见 [.env.example](.env.example)。程序**不自动读取** `.env` 文件，请由 shell、systemd 或容器运行时注入。`.env` 已列入 `.gitignore`，不会进入版本库，只提交 `.env.example`。

| 变量 | 默认 | 说明 |
|---|---|---|
| `GN_ENV` | `dev` | `dev`/`staging`/`prod`；非 `dev`（`staging`/`prod`）禁止任何 Fake 提供者 |
| `GN_MODE` | `all` | `all`/`api`/`worker`；`api` 只提供 HTTP，`worker` 只运行转换 Worker |
| `GN_HTTP_ADDR` | `0.0.0.0:8080` | 监听地址 |
| `GN_PUBLIC_BASE_URL` | `https://gleannote.example` | 生成分享链接对外地址的前缀，拼接为 `{base}/s/{token}`；不能为空 |
| `GN_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `GN_HMAC_SECRET` | dev 内置值 | 验证码摘要密钥，至少 32 字节；`staging`/`prod` 必须显式注入 |
| `GN_JWT_PRIVATE_KEY` | 空 | Ed25519 种子的 base64（32 字节）；留空则进程内生成随机密钥，重启后旧令牌失效；非 `dev` 必填 |
| `GN_VISION_PROVIDER` | `fake` | 视觉识别提供者：`fake` 或 `deepseek`（OpenAI Chat Completions 兼容多模态） |
| `GN_VISION_API_KEY` | 空 | 视觉供应商密钥；选择 `deepseek` 时必填，只存服务端 |
| `GN_VISION_BASE_URL` | 空 | 留空用 `https://api.deepseek.com`；可指向任意兼容端点 |
| `GN_VISION_MODEL` | 空 | 留空用 `deepseek-flash` |
| `GN_VISION_REQUEST_TIMEOUT` | `2m` | 单次模型调用超时；整篇转换仍受 5 分钟上限约束 |
| `GN_MAILER_PROVIDER` | `fake` | 邮件提供者，当前仅 `fake` |
| `GN_FAKE_MAIL_REVEAL_CODE` | `false` | 开发态邮件日志是否输出明文邮箱与验证码；默认脱敏，仅本地手工联调时开启 |
| `GN_OBJECT_STORE_PROVIDER` | `minio` | 对象存储提供者：`minio`/`s3` 或 `fake` |
| `GN_S3_ENDPOINT` | `127.0.0.1:9000` | MinIO / S3 端点，可带 http(s) 协议 |
| `GN_S3_BUCKET` | `gleannote-private` | 私有桶名，缺失时启动自动创建 |
| `GN_S3_ACCESS_KEY` | 空 | 访问密钥，使用 minio 时必填 |
| `GN_S3_SECRET_KEY` | 空 | 私钥，使用 minio 时必填 |
| `GN_S3_REGION` | `us-east-1` | 区域标识 |
| `GN_S3_USE_SSL` | `false` | 是否使用 HTTPS；端点显式写协议时以协议为准 |
| `GN_FAKE_VISION_LATENCY` | `0s` | Fake 识图延迟，如 `2s` |
| `GN_FAKE_VISION_FAILURE` | 空 | Fake 故障注入，取值见下 |

## 付费能力如何接入（provider 策略）

付费能力通过端口抽象接入：视觉支持 DeepSeek（OpenAI Chat Completions 兼容，默认模型 `deepseek-flash`，按调用计费）；邮件当前仅提供 `fake`；对象存储对接自托管 MinIO（S3 兼容）。业务代码只依赖端口接口，供应商实现放在 `internal/infrastructure/<capability>/`，由启动装配按名称选择：

| 能力 | 端口 | 可选实现 | 说明 |
|---|---|---|---|
| 图片 → Markdown | `port.VisionProvider` | `fake` / `deepseek` | `fake` 为确定性输出；`deepseek` 下载预签名页面图并以 base64 内联调用多模态模型，按阅读顺序还原标题层级、列表、表格、公式与代码块，每页以「## 第 N 页」分段；未填 Key 时仍可离线使用 `fake` |
| 邮件（验证码） | `port.Mailer` | `fake` | 单行 JSON 写入标准输出，不投递、不计费；默认对邮箱与验证码脱敏，`GN_FAKE_MAIL_REVEAL_CODE=true` 时输出明文供本地联调 |
| 对象存储 | `port.ObjectStore` | `minio`（`fake` 仅测试） | 对接本地 MinIO 容器；S3 协议，桶私有，访问走短期预签名地址 |

MinIO 适配器位于 `internal/infrastructure/objectstore/minio.go`：启动时校验桶并自动创建，`PresignPut` 生成上传地址，`Stat`/`Delete` 直接操作对象，`Health` 检查桶可达性。`fake` 实现保留给单元测试与无容器场景（`GN_OBJECT_STORE_PROVIDER=fake`）。

启用真实识图（`.env` 或 shell 注入均可）：

```bash
GN_VISION_PROVIDER=deepseek
GN_VISION_API_KEY=sk-...          # 选择 deepseek 时必填，缺少会启动失败
# 可选：GN_VISION_BASE_URL=https://api.deepseek.com、GN_VISION_MODEL=deepseek-flash
```

`deepseek` 适配器位于 `internal/infrastructure/vision/deepseek.go`：逐页下载预签名原图，每 4 页合并一次请求，以 `data:<mime>;base64,...` 内联提交；提示词要求按阅读顺序还原标题层级、列表、表格、公式与代码块，每页以「## 第 N 页」分段；模型输出整体被三反引号包裹时自动剥离，未识别页按「（第 N 页未识别到文字）」提取页码。容器内需能直连 `api.deepseek.com`；宿主走代理时给 `api` 容器注入 `HTTPS_PROXY`（`NO_PROXY` 已包含内部服务名）。

接入真实供应商时只允许改两处：新增适配器实现，在对应能力的 `New` 中登记名称；端口签名、用例与 DTO 不变。规则全文见 [ARCHITECTURE.md 8.16](docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略)。

两条硬约束：

1. 未实现的提供者名称会让进程**启动失败**（`provider not configured`），不会静默降级成 Fake；
2. `GN_ENV=staging|prod` 时任一提供者为 `fake`，进程同样启动失败。

## 故障演练

通过环境变量注入，无需改代码。以「视觉供应商不可用」为例：

```powershell
$env:GN_FAKE_VISION_FAILURE="unavailable"; go run ./cmd/api
curl.exe -s -i http://127.0.0.1:8080/readyz
```

`/readyz` 返回 `503`，`checks.vision` 为 `provider unavailable`。可用取值：

| 值 | Fake 行为 | 影响的端口错误 |
|---|---|---|
| `unavailable` | 识图与健康检查均失败 | `ErrProviderUnavailable` |
| `unreadable` | 识图失败，健康检查仍正常 | `ErrImageUnreadable` |
| `unsupported` | 识图失败，健康检查仍正常 | `ErrContentUnsupported` |
| 空 | 正常返回确定性结果 | — |

`GN_FAKE_VISION_LATENCY=2s` 用于联调「转换中」状态与超时行为。

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

验证结果与覆盖率见 [TEST-REPORT.md](docs/TEST-REPORT.md)。

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

## 常见问题

**启动时出现 `[GIN-debug]` 提示？** `dev` 环境保留调试模式用于打印路由表；`staging`/`prod` 自动切换 release 模式。

**`/readyz` 返回 503？** 看 `checks` 里哪一项不是 `ok`。`objectStore` 失败通常是 MinIO 容器未启动或凭据不匹配；`vision` 失败最常见原因是设置了 `GN_FAKE_VISION_FAILURE=unavailable`。

**启动提示 `装配对象存储: ...`？** 说明连不上 MinIO 或桶不可用。依次确认：容器在运行（`docker ps`）、`GN_S3_ENDPOINT` 与 `GN_S3_ACCESS_KEY`/`GN_S3_SECRET_KEY` 与容器启动参数一致、`GN_S3_BUCKET` 名称合法。

**提示 `provider not configured`？** 配置了没有对应实现的提供者名称。视觉接受 `fake`/`deepseek`（`deepseek` 还需 `GN_VISION_API_KEY`），邮件只接受 `fake`，对象存储只接受 `minio`/`s3`/`fake`。

**转换任务一直停在 `QUEUED`？** `GN_MODE=api` 只提供 HTTP，不消费队列；用 `all` 或在另一进程以 `worker` 启动。队列随 `GN_CACHE_PROVIDER` 选择：默认 `memory` 为进程内实现，拆进程部署不共享；配置 `GN_CACHE_PROVIDER=redis` 后经 Redis 列表（BLMOVE 可靠队列）跨进程共享。

**Fake 的对象与邮件会保留吗？** 使用 `GN_OBJECT_STORE_PROVIDER=fake` 时对象只存在内存，进程重启即丢失；邮件只写标准输出。默认的 minio 模式下对象持久化在容器卷 `gleannote-minio-data`。

**docker-compose 在哪？** 根目录 `compose.yaml` 已覆盖 API/Worker + PostgreSQL + Redis + MinIO 的本地编排，配置集中在 `.env`（样例 `.env.example`）；见「启动开发服务 → 0. 一键启动」。

## 许可

本项目采用 [Apache License 2.0](LICENSE)。贡献代码前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。
