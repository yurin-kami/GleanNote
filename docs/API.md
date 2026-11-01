# 拾页笔记（GleanNote）服务端接口文档

## 1. 文档信息与依据

| 项目 | 内容 |
|---|---|
| 文档编号 | GN-API-001 |
| 版本 | v1.1 |
| 状态 | 已评审基线（交付口径） |
| 更新日期 | 2026-11-01 |
| 适用服务 | `https://{host}/v1`，仅服务端 API；不含客户端内部协议 |
| 架构依据 | [ARCHITECTURE.md](ARCHITECTURE.md)（尤其 8.1–8.4、8.15） |
| 需求依据 | [PRD-拾页笔记-GleanNote.md](PRD-拾页笔记-GleanNote.md) v1.2 |
| 机器可读契约 | [openapi.yaml](openapi.yaml)，本文字段与契约必须保持一致，冲突时以契约文件与本文修订记录为准 |

本文字段定义的强制力来自以下规范，落地口径以 [ARCHITECTURE.md](ARCHITECTURE.md) 第 8 章为准：

| 规范 | 出处 | 本文对应章节 |
|---|---|---|
| RFC 9110 HTTP Semantics | <https://www.rfc-editor.org/rfc/rfc9110.html> | 2.1、2.4、2.8 |
| RFC 9457 Problem Details | <https://www.rfc-editor.org/rfc/rfc9457.html> | 2.10 |
| Google AIP-158 分页 | <https://google.aip.dev/158> | 2.6 |
| Google AIP-160 过滤 | <https://google.aip.dev/160> | 2.7 |
| Google AIP-136 自定义方法 | <https://google.aip.dev/136> | 2.1、5 |
| Google AIP-164 软删除 | <https://google.aip.dev/164> | 6.4、6.5 |
| Google AIP-193 错误 | <https://google.aip.dev/193> | 2.10、8 |
| Google AIP-180 兼容性 | <https://google.aip.dev/180> | 7 |
| Microsoft REST API Guidelines | <https://github.com/microsoft/api-guidelines> | 2.4、2.6 |

## 2. 通用约定

### 2.1 基础约定

| 项目 | 约定 |
|---|---|
| 基础路径 | `/v1`，版本只出现在路径中，不接受 `Accept-Version` 等头部版本协商 |
| 传输 | 仅 HTTPS，TLS 1.2+ |
| 请求体 | `application/json; charset=utf-8`；上传为预签名 `PUT`，不经过应用 |
| 响应体 | 成功：直接返回资源对象或分页集合；失败：`application/problem+json` |
| 字段命名 | `camelCase` |
| 枚举值 | `UPPER_SNAKE_CASE`，客户端必须容忍未知值 |
| 资源 ID | UUIDv7 字符串，形如 `018f2c1e-8a3b-7c4d-9e10-2b3c4d5e6f70` |
| 时间 | RFC 3339 UTC，带 `Z`，形如 `2026-10-28T06:00:00Z` |
| 字节数 | 整数字节，`bigint` 语义（JSON number，安全整数范围内） |
| 空值 | 无值字段直接省略；`null` 仅用于「显式清空」语义 |
| 自定义方法 | `POST /v1/{collection}/{id}:{verb}`，动词为小驼峰，禁止介词（AIP-136） |
| 最大请求体 | 12MB（超过返回 `413`） |
| 字符计数 | 按 Unicode 字符计，中文、英文、符号各计 1（PRD 5.1） |

### 2.2 认证

除标注「无鉴权」的端点外，全部要求：

```http
Authorization: Bearer {accessToken}
```

Access Token 有效期 15 分钟；过期返回 `401 AUTH_TOKEN_EXPIRED`，客户端用 Refresh Token 调用 `POST /v1/auth:refresh` 换取新令牌后重放原请求。Refresh Token 每次使用即轮换，旧值立即失效。

### 2.3 请求与响应头

| 头部 | 方向 | 必选 | 说明 |
|---|---|---|---|
| `Authorization` | 请求 | 鉴权端点必选 | `Bearer {accessToken}` |
| `X-Request-Id` | 请求 | 否 | 客户端可自带；服务端始终回显，未带则生成 |
| `traceparent` | 请求 | 否 | W3C 追踪上下文，服务端透传 |
| `Idempotency-Key` | 请求 | 创建类必选 | 8–64 位可见 ASCII，24 小时内唯一 |
| `If-Match` | 请求 | 否 | 形如 `"rev-7"`，与请求体 `revision` 二选一 |
| `X-Client-Version` | 请求 | 否 | 客户端版本，用于灰度与问题定位 |
| `X-Device-Id` | 请求 | 否 | 设备标识，用于刷新令牌绑定与登录态吊销 |
| `X-Request-Id` | 响应 | 是 | 与 `problem.requestId` 一致 |
| `Retry-After` | 响应 | 条件 | `429`、`503` 时返回秒数 |
| `Deprecation` / `Sunset` | 响应 | 条件 | 端点废弃时返回（RFC 8594） |

### 2.4 分页

遵循 AIP-158。所有集合端点均分页，且从一开始就支持。

| 参数 | 类型 | 默认 | 约束 | 说明 |
|---|---|---|---|---|
| `pageSize` | int32 | 20 | 1–100；>100 自动收敛为 100；<0 返回 `400` | 每页条数 |
| `pageToken` | string | 空 | 不透明，1 小时有效 | 上一页返回的 `nextPageToken` |

响应集合结构：

```json
{
  "items": [],
  "nextPageToken": "eyJvIjoyMCwicyI6InVwZGF0ZVRpbWUiLCJ2IjoxfQ",
  "totalSize": 128
}
```

- `nextPageToken` 为空字符串表示已到末页，这是判断结束的唯一依据。
- `totalSize` 为估算值，客户端不得用于计算页数后直接跳页。

### 2.5 排序与过滤

`orderBy` 仅接受白名单，非白名单返回 `400 VALIDATION_FAILED`：

| 端点 | 允许值 | 默认 |
|---|---|---|
| 笔记列表 | `updateTime desc`、`createTime desc`、`title asc` | `updateTime desc` |
| 目录子项 | `createTime asc`、`name asc` | `createTime asc` |
| 回收站 | `deleteTime desc` | `deleteTime desc` |
| 版本列表 | `createTime desc` | `createTime desc` |
| 标签列表 | `noteCount desc`、`name asc` | `noteCount desc` |

过滤参数为显式强类型参数（`folderId`、`state`、`tagIds`、`updatedAfter`），不支持通用表达式语法；多值参数用重复键传递（`tagIds=a&tagIds=b`），语义为「同时包含」。

### 2.6 幂等

所有创建类 `POST` 必须携带 `Idempotency-Key`：

| 场景 | 结果 |
|---|---|
| 同用户 + 同键 + 同请求体，24 小时内重复 | 返回首次响应（含首次状态码） |
| 同用户 + 同键 + 不同请求体 | `409 IDEMPOTENCY_KEY_REUSED` |
| 键格式非法 | `400 VALIDATION_FAILED` |

上传的预签名 `PUT` 本身幂等；`complete` 幂等（同 `uploadId` 重复调用返回同一结果）。

### 2.7 乐观锁与冲突

笔记更新必须携带 `revision`（或 `If-Match: "rev-{n}"`），语义按 [ARCHITECTURE.md](ARCHITECTURE.md) 6.3：

- `revision` 匹配：正常更新，返回新 `revision`。
- `revision` 落后：服务端把当前正文另存为冲突副本，再写入本次内容，响应 `200` 并附 `conflictCopyId`。**不返回 412**。
- 目录改名、标签改名等无冲突语义的更新：`revision` 不匹配返回 `412 REVISION_MISMATCH`。

### 2.8 错误模型

错误响应统一为 `application/problem+json`，字段定义见 [ARCHITECTURE.md](ARCHITECTURE.md) 8.2：

```json
{
  "type": "https://gleannote.example/problems/quota-exceeded",
  "title": "Daily quota exceeded",
  "status": 409,
  "detail": "Requested 12 pages but only 5 remain for 2026-10-28.",
  "instance": "/v1/notes:createFromPages#req_01JB7Q2K9S",
  "code": "QUOTA_EXCEEDED",
  "userMessage": "今日额度剩余 5 张，可减少图片后重试",
  "metadata": { "remaining": 5, "requested": 12, "resetAt": "2026-10-28T16:00:00Z" },
  "retryable": false,
  "requestId": "req_01JB7Q2K9S"
}
```

约束：

1. 客户端按 `code` 分支，不解析 `detail`、`userMessage`。
2. 业务变量只从 `metadata` 读取。
3. 5xx 一律 `retryable=true`，且不泄露内部细节。
4. 未找到或不属于当前用户的资源一律 `404 RESOURCE_NOT_FOUND`（防枚举，见 ADR-014）。

### 2.9 图片上传协议

三步：申请会话 → 直传 MinIO → 确认。

1. `POST /v1/uploads` 批量申请，返回每张图的 `uploadId` 与预签名 `PUT` 地址及必须携带的请求头。
2. 客户端直接 `PUT` 二进制到 `putUrl`，**不得** 改动 `headers` 中的 `Content-Type`。
3. `POST /v1/uploads/{uploadId}:complete` 触发服务端校验与缩略图生成，成功后该上传可被笔记引用。

上传会话有效期 30 分钟；过期后 `complete` 返回 `410 UPLOAD_EXPIRED`。单张上限 10MB（PRD 5.2），格式仅 JPG/PNG/WEBP（HEIC 由客户端转 JPEG）。

## 3. 资源模型与枚举

### 3.1 资源与端点归属

| 资源 | 集合路径 | 说明 |
|---|---|---|
| 用户 | `/v1/users/me` | 单例，无列表 |
| 目录 | `/v1/folders` | 可嵌套，最多 5 层 |
| 笔记 | `/v1/notes` | 归属唯一目录 |
| 页面图片 | `/v1/notes/{noteId}/pages` | 只读；写入经上传会话 |
| 转换任务 | `/v1/notes/{noteId}/conversionJob` | 单例，只保留最近一次 |
| 标签 | `/v1/tags` | 账号级共享 |
| 版本 | `/v1/notes/{noteId}/versions` | 只读 + 恢复 |
| 分享链接 | `/v1/notes/{noteId}/shareLink` | 单例 |
| 回收站 | `/v1/trash` | 跨类型聚合视图 |
| 变更流 | `/v1/sync:changes` | 增量同步 |

### 3.2 枚举字典

| 枚举 | 取值 | 说明 |
|---|---|---|
| `NoteState` | `DRAFT`、`NORMAL`、`DELETED` | `DELETED` 仅出现在回收站与增量同步 |
| `JobState` | `QUEUED`、`RUNNING`、`SUCCEEDED`、`FAILED`、`CANCELED` | 与 PRD 3.3 一致 |
| `JobPhase` | `QUEUED`、`RECOGNIZING`、`GENERATING`、`COMPLETED` | 展示文案用 |
| `JobFailureCode` | `PROVIDER_UNAVAILABLE`、`IMAGE_UNREADABLE`、`CONTENT_UNSUPPORTED`、`TIMEOUT`、`EMPTY_RESULT`、`QUOTA_EXCEEDED`、`STORAGE_EXCEEDED`、`NOTE_MODIFIED`、`INTERNAL_ERROR` | 对应 PRD FR-05 失败类型；后两者为结算期并发冲突与基础设施故障 |
| `JobKind` | `CREATE`、`APPEND` | 新建转换 / 追加页面 |
| `VersionSource` | `CONVERSION`、`MANUAL_SAVE`、`AUTO_SAVE`、`CONFLICT_COPY`、`RESTORE` | 版本来源 |
| `UploadStatus` | `PENDING`、`UPLOADED`、`READY`、`EXPIRED` | `READY` 表示校验与缩略图完成 |
| `TrashItemType` | `FOLDER`、`NOTE` | 回收站条目类型 |
| `ChangeOp` | `CREATE`、`UPDATE`、`DELETE` | 增量同步事件类型 |
| `ResourceType` | `FOLDER`、`NOTE`、`TAG`、`PAGE` | 同步资源类型 |
| `VerificationPurpose` | `REGISTER`、`RESET_PASSWORD`、`DELETE_ACCOUNT` | 验证码用途 |
| `ExportFormat` | `MARKDOWN_ZIP`、`PDF` | P1 导出格式 |
| `ImportMode` | `PER_IMAGE`、`MERGE` | P1 批量导入模式 |

## 4. 接口总览

标注：`A` = 需鉴权；`I` = 需 `Idempotency-Key`；`P0/P1` = 交付阶段。

### 4.1 账号与认证

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 1 | `POST /v1/auth:sendCode` | 发送邮箱验证码 | 否 | 否 | P0 |
| 2 | `POST /v1/auth:register` | 注册并登录 | 否 | 是 | P0 |
| 3 | `POST /v1/auth:login` | 登录 | 否 | 否 | P0 |
| 4 | `POST /v1/auth:refresh` | 刷新令牌 | 否 | 否 | P0 |
| 5 | `POST /v1/auth:logout` | 退出登录 | 是 | 否 | P0 |
| 6 | `POST /v1/auth:resetPassword` | 通过验证码重置密码 | 否 | 是 | P0 |
| 7 | `GET /v1/users/me` | 当前用户 | 是 | 否 | P0 |
| 8 | `PATCH /v1/users/me` | 修改昵称 | 是 | 否 | P0 |
| 9 | `DELETE /v1/users/me` | 注销账号（验证码二次确认） | 是 | 否 | P0 |
| 10 | `GET /v1/users/me/quota` | 额度与云端容量 | 是 | 否 | P0 |

### 4.2 目录

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 11 | `GET /v1/folders` | 列出某父级下的子目录（分页） | 是 | 否 | P0 |
| 12 | `POST /v1/folders` | 新建目录 | 是 | 是 | P0 |
| 13 | `GET /v1/folders/{folderId}` | 目录详情 | 是 | 否 | P0 |
| 14 | `GET /v1/folders/{folderId}/path` | 面包屑与原路径 | 是 | 否 | P0 |
| 15 | `PATCH /v1/folders/{folderId}` | 重命名 | 是 | 否 | P0 |
| 16 | `DELETE /v1/folders/{folderId}` | 删除目录（整棵子树软删除） | 是 | 否 | P0 |
| 17 | `POST /v1/folders/{folderId}:move` | 移动到目标父级 | 是 | 否 | P0 |
| 18 | `POST /v1/folders/{folderId}:undelete` | 从回收站恢复 | 是 | 否 | P0 |
| 19 | `POST /v1/folders/{folderId}:expunge` | 彻底删除 | 是 | 否 | P0 |

### 4.3 笔记

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 20 | `GET /v1/notes` | 目录内笔记列表 | 是 | 否 | P0 |
| 21 | `POST /v1/notes` | 新建空白笔记（含离线创建补录） | 是 | 是 | P0 |
| 22 | `POST /v1/notes:createFromPages` | 由已上传图片创建草稿并提交转换 | 是 | 是 | P0 |
| 23 | `GET /v1/notes/{noteId}` | 笔记详情（含正文与标签） | 是 | 否 | P0 |
| 24 | `PATCH /v1/notes/{noteId}` | 保存标题/正文（乐观锁） | 是 | 是 | P0 |
| 25 | `DELETE /v1/notes/{noteId}` | 删除到回收站 | 是 | 否 | P0 |
| 26 | `POST /v1/notes/{noteId}:move` | 移动笔记 | 是 | 否 | P0 |
| 27 | `POST /v1/notes/{noteId}:undelete` | 从回收站恢复 | 是 | 否 | P0 |
| 28 | `POST /v1/notes/{noteId}:expunge` | 彻底删除 | 是 | 否 | P0 |
| 29 | `POST /v1/notes/{noteId}:appendPages` | 追加页面并提交追加转换 | 是 | 是 | P1 |
| 30 | `GET /v1/notes:search` | 全文搜索 | 是 | 否 | P1 |

### 4.4 图片与转换

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 31 | `POST /v1/uploads` | 申请上传会话与预签名地址 | 是 | 否 | P0 |
| 32 | `POST /v1/uploads/{uploadId}:complete` | 确认上传并生成缩略图 | 是 | 否 | P0 |
| 33 | `GET /v1/notes/{noteId}/pages` | 页面图片列表（缩略图） | 是 | 否 | P0 |
| 34 | `GET /v1/notes/{noteId}/pages/{pageId}` | 单页原图地址 | 是 | 否 | P0 |
| 35 | `GET /v1/notes/{noteId}/conversionJob` | 最近一次转换任务 | 是 | 否 | P0 |
| 36 | `POST /v1/notes/{noteId}/conversionJob:cancel` | 取消任务 | 是 | 否 | P0 |
| 37 | `POST /v1/notes/{noteId}/conversionJob:retry` | 重试（复用已上传图片） | 是 | 是 | P0 |

### 4.5 回收站、标签、版本、分享、同步

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 38 | `GET /v1/trash` | 回收站列表（批次聚合） | 是 | 否 | P0 |
| 39 | `GET /v1/tags` | 标签列表 | 是 | 否 | P1 |
| 40 | `POST /v1/tags` | 新建标签 | 是 | 是 | P1 |
| 41 | `PATCH /v1/tags/{tagId}` | 重命名标签 | 是 | 否 | P1 |
| 42 | `DELETE /v1/tags/{tagId}` | 删除标签 | 是 | 否 | P1 |
| 43 | `PUT /v1/notes/{noteId}/tags` | 覆盖设置笔记标签（≤10） | 是 | 否 | P1 |
| 44 | `GET /v1/notes/{noteId}/versions` | 版本列表 | 是 | 否 | P1 |
| 45 | `GET /v1/notes/{noteId}/versions/{versionId}` | 版本正文 | 是 | 否 | P1 |
| 46 | `POST /v1/notes/{noteId}/versions/{versionId}:restore` | 恢复版本 | 是 | 是 | P1 |
| 47 | `GET /v1/notes/{noteId}/shareLink` | 查询分享状态 | 是 | 否 | P1 |
| 48 | `POST /v1/notes/{noteId}/shareLink` | 开启分享 | 是 | 是 | P1 |
| 49 | `DELETE /v1/notes/{noteId}/shareLink` | 关闭分享 | 是 | 否 | P1 |
| 50 | `GET /v1/public/shares/{token}` | 公开读取被分享笔记（JSON） | 否 | 否 | P1 |
| 50a | `GET /s/{token}` | 公开分享页（服务端渲染只读 HTML） | 否 | 否 | P1 |
| 51 | `GET /v1/sync:changes` | 增量同步 | 是 | 否 | P0 |

### 4.6 导出、导入与运维

| # | 方法与路径 | 用途 | A | I | 阶段 |
|---|---|---|---|---|---|
| 52 | `POST /v1/exports` | 发起导出任务 | 是 | 是 | P1 |
| 53 | `GET /v1/exports/{exportId}` | 查询导出结果与下载地址 | 是 | 否 | P1 |
| 54 | `POST /v1/imports` | 发起批量导入任务 | 是 | 是 | P1 |
| 55 | `GET /v1/imports/{importId}` | 查询导入进度 | 是 | 否 | P1 |
| 56 | `GET /healthz` | 存活探针 | 否 | 否 | P0 |
| 57 | `GET /readyz` | 就绪探针（含依赖检查） | 否 | 否 | P0 |

## 5. 接口定义

### 5.1 账号与认证

#### 5.1.1 `POST /v1/auth:sendCode`

请求：

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `email` | string | 是 | RFC 5322 邮箱，≤254 字符，小写化存储 |
| `purpose` | enum | 是 | `REGISTER`、`RESET_PASSWORD`、`DELETE_ACCOUNT` |

响应 `204`，不区分邮箱是否已注册（避免账号枚举）。错误：`VALIDATION_FAILED`(400)、`AUTH_CODE_RATE_LIMITED`(429)。

#### 5.1.2 `POST /v1/auth:register`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `email` | string | 是 | 未注册过的邮箱 |
| `code` | string | 是 | 6 位数字 |
| `password` | string | 是 | 8–32 位，同时含字母与数字 |
| `nickname` | string | 否 | 1–20 字符，缺省取邮箱 `@` 前缀 |
| `agreements` | object | 是 | `{ "userAgreement": true, "privacyPolicy": true }`，必须全为 `true` |
| `deviceId` | string | 否 | 设备标识 |

响应 `201`：

```json
{
  "user": { "id": "018f...", "email": "a@b.com", "nickname": "a", "createdAt": "2026-10-28T06:00:00Z" },
  "accessToken": "eyJ...",
  "refreshToken": "rt_7f3c...",
  "expiresIn": 900,
  "tokenType": "Bearer"
}
```

副作用：自动创建根级目录「我的笔记」（PRD FR-01）。
错误：`AUTH_CODE_INVALID`、`AUTH_CODE_EXPIRED`、`AUTH_EMAIL_ALREADY_EXISTS`、`VALIDATION_FAILED`、`RATE_LIMITED`。

#### 5.1.3 `POST /v1/auth:login`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `email` | string | 是 | — |
| `password` | string | 是 | — |
| `deviceId` | string | 否 | 未带则按 IP 维度管理登录态 |

响应 `200`，结构同注册（不含 `user.createdAt` 以外的额外字段）。
错误：`AUTH_INVALID_CREDENTIALS`(401，未注册邮箱与口令错误返回同一码)、`AUTH_TOO_MANY_ATTEMPTS`(429，含 `retryAfterSeconds`)、`VALIDATION_FAILED`。

#### 5.1.4 `POST /v1/auth:refresh`

请求 `{ "refreshToken": "rt_...", "deviceId": "..." }` → `200` 返回新的 `accessToken`、`refreshToken`、`expiresIn`。
错误：`AUTH_TOKEN_INVALID`(401)、`AUTH_REFRESH_REUSED`(401，触发该设备全部令牌吊销)。

#### 5.1.5 `POST /v1/auth:logout`

请求 `{ "refreshToken": "rt_...", "allDevices": false }` → `204`。`allDevices=true` 时吊销该用户全部刷新令牌。

#### 5.1.6 `POST /v1/auth:resetPassword`

请求 `{ "email", "code", "newPassword" }` → `204`。重置成功后该用户全部刷新令牌失效（PRD FR-01）。
错误：`AUTH_CODE_INVALID`(400，未注册邮箱与验证码错误返回同一码)、`AUTH_CODE_EXPIRED`(400)、`VALIDATION_FAILED`(400)、`RATE_LIMITED`(429)。

#### 5.1.7 用户与额度

`GET /v1/users/me` → `200`

```json
{
  "id": "018f...",
  "email": "a@b.com",
  "nickname": "a",
  "emailVerified": true,
  "createdAt": "2026-10-28T06:00:00Z",
  "updatedAt": "2026-10-28T06:00:00Z"
}
```

`PATCH /v1/users/me` 请求 `{ "nickname": "新昵称" }` → `200` 同上结构。

`DELETE /v1/users/me` 请求 `{ "code": "123456", "confirm": true }` → `204`。物理删除并异步清理对象存储（PRD FR-01、5.7）。

`GET /v1/users/me/quota` → `200`

```json
{
  "quotaDate": "2026-10-28",
  "dailyLimit": 50,
  "remaining": 38,
  "resetAt": "2026-10-28T16:00:00Z",
  "conversionRunning": true,
  "storage": { "usedBytes": 73400320, "limitBytes": 209715200, "usedPercent": 35 }
}
```

`resetAt` 为北京时间次日 0 点对应的 UTC 时刻。

### 5.2 目录

#### 5.2.1 目录对象

```json
{
  "id": "018f...",
  "parentId": "018f...",
  "name": "线性代数",
  "depth": 2,
  "revision": 3,
  "childCount": 0,
  "noteCount": 12,
  "createdAt": "2026-10-28T06:00:00Z",
  "updatedAt": "2026-10-28T06:00:00Z"
}
```

#### 5.2.2 `GET /v1/folders`

参数：`parentId`（可选，缺省表示根级）、`pageSize`、`pageToken`、`orderBy`。
响应：`Folder` 集合。根级目录即 PRD 中的第 1 层，`depth=1`。

#### 5.2.3 `POST /v1/folders`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `parentId` | string | 否 | 缺省为根级；必须存在且 `depth ≤ 4` |
| `name` | string | 是 | 1–50 字符；禁止 `/ \ : * ? " < > \|`；无首尾空格；同父级唯一 |

响应 `201` `Folder`。
错误：`FOLDER_DEPTH_EXCEEDED`(409)、`NAME_DUPLICATED`(409)、`VALIDATION_FAILED`(400)。

#### 5.2.4 `GET /v1/folders/{folderId}/path`

响应 `200`：

```json
{
  "items": [
    { "id": "018f...", "name": "我的笔记" },
    { "id": "018f...", "name": "数学" },
    { "id": "018f...", "name": "线性代数" }
  ],
  "displayPath": "我的笔记 / 数学 / 线性代数"
}
```

#### 5.2.5 `PATCH /v1/folders/{folderId}`

请求 `{ "name": "新名称", "revision": 3 }` → `200` `Folder`。同名、超长、非法字符按 PRD FR-02 返回对应错误码。

#### 5.2.6 `POST /v1/folders/{folderId}:move`

请求 `{ "targetParentId": "018f..." | null }`（`null` 表示移到根级）。

响应 `200`：

```json
{ "folder": { "id": "018f...", "parentId": "018f...", "depth": 3 }, "movedFolderCount": 4, "movedNoteCount": 37 }
```

错误：`FOLDER_CYCLE`(409，目标为自身或子孙)、`FOLDER_DEPTH_EXCEEDED`(409)、`NAME_DUPLICATED`(409)。

#### 5.2.7 `DELETE /v1/folders/{folderId}`

软删除整棵子树，合并为一条回收站记录（PRD FR-02、FR-09）。响应 `200`：

```json
{ "deleteBatchId": "018f...", "folderCount": 4, "noteCount": 37, "purgeAt": "2026-11-27T06:00:00Z" }
```

删除时该子树内笔记的分享链接立即失效（PRD 5.4）。

#### 5.2.8 恢复与彻底删除

`POST /v1/folders/{folderId}:undelete` → `200` `Folder`，响应头附 `restoredToRoot`（布尔，以响应体字段 `restoredToRoot` 呈现）。恢复整棵子树，同级重名时名称追加 ` (2)`。

`POST /v1/folders/{folderId}:expunge` → `204`。仅允许对 `DELETED` 状态调用；否则 `409 FAILED_PRECONDITION`。

### 5.3 笔记

#### 5.3.1 笔记对象

列表项（`NoteSummary`）：

```json
{
  "id": "018f...",
  "folderId": "018f...",
  "folderPath": "我的笔记 / 数学 / 线性代数",
  "title": "矩阵的秩",
  "state": "NORMAL",
  "revision": 8,
  "charCount": 4213,
  "pageCount": 3,
  "thumbnailUrl": "https://minio.example/signed?...",
  "tags": [{ "id": "018f...", "name": "考研" }],
  "isConflictCopy": false,
  "createdAt": "2026-10-28T06:00:00Z",
  "updatedAt": "2026-10-28T06:30:00Z"
}
```

详情（`NoteDetail`）= `NoteSummary` + `content`（Markdown 原文）+ `shareEnabled`（P1）。列表接口**不返回** `content`。

#### 5.3.2 `GET /v1/notes`

参数：`folderId`（必选）、`state`（可选，默认排除 `DELETED`）、`orderBy`、`pageSize`、`pageToken`、`updatedAfter`（增量刷新用）。
响应：`NoteSummary` 集合。分页默认 20（PRD FR-07）。

#### 5.3.3 `POST /v1/notes`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `folderId` | string | 是 | 目标目录 |
| `title` | string | 否 | ≤100 字符，缺省空串（展示为「未命名笔记」） |
| `content` | string | 否 | ≤20 万字符 |
| `clientCreatedAt` | string | 否 | 离线创建时的本地时间，用于排序纠偏 |

响应 `201` `NoteDetail`，`state=NORMAL`，`revision=1`。

#### 5.3.4 `POST /v1/notes:createFromPages`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `folderId` | string | 是 | 目标目录 |
| `uploadIds` | string[] | 是 | 1–20 个，全部状态为 `READY`，顺序即页面顺序 |
| `title` | string | 否 | 缺省由转换结果生成（PRD FR-05） |

响应 `201`：

```json
{
  "note": { "id": "018f...", "state": "DRAFT", "folderId": "018f...", "revision": 1, "title": "未命名笔记" },
  "conversionJob": { "id": "018f...", "state": "QUEUED", "phase": "QUEUED", "totalPages": 3, "processedPages": 0 },
  "quota": { "remaining": 47, "charged": 3 }
}
```

校验顺序（必须与 [ARCHITECTURE.md](ARCHITECTURE.md) 8.8 一致）：额度 → 存储容量 → 上传有效性 → 事务写入（笔记 + 页面 + 任务 + 账本 + 变更日志）。
错误：`QUOTA_EXCEEDED`(409)、`STORAGE_EXCEEDED`(409)、`UPLOAD_NOT_READY`(409)、`NOTE_PAGE_LIMIT_EXCEEDED`(409)。

#### 5.3.5 `GET /v1/notes/{noteId}`

响应 `200` `NoteDetail`。已删除笔记在回收站上下文中仍可读（`state=DELETED`）；已彻底删除返回 `404`。

#### 5.3.6 `PATCH /v1/notes/{noteId}`

| 字段 | 类型 | 必选 | 约束 |
|---|---|---|---|
| `title` | string | 否 | ≤100 字符 |
| `content` | string | 否 | ≤20 万字符 |
| `revision` | int32 | 是 | 客户端所基于的版本号 |
| `saveSource` | enum | 否 | `MANUAL_SAVE`（默认）、`AUTO_SAVE`；服务端据此决定是否生成版本 |

响应 `200` `NoteDetail`，附 `conflictCopyId`（若发生冲突）：

```json
{
  "note": { "id": "018f...", "revision": 9 },
  "conflictCopyId": "018f..."
}
```

规则：`AUTO_SAVE` 同一笔记两次版本间隔 < 5 分钟时只更新正文、不生成版本（PRD FR-16）；`MANUAL_SAVE` 与恢复操作总是生成版本。
错误：`NOTE_TOO_LARGE`(413)、`NOTE_STATE_INVALID`(409，非 `NORMAL`/`DRAFT` 不可编辑)。

#### 5.3.7 `DELETE /v1/notes/{noteId}` → `200`

```json
{ "deleteBatchId": "018f...", "purgeAt": "2026-11-27T06:00:00Z" }
```

#### 5.3.8 `POST /v1/notes/{noteId}:move`

请求 `{ "folderId": "018f..." }` → `200` `NoteSummary`。

#### 5.3.9 恢复与彻底删除

`POST /v1/notes/{noteId}:undelete` → `200` `NoteDetail`。原目录仍在回收站时同时恢复必要祖先；原目录已彻底删除时恢复到根级并返回 `restoredToRoot=true`（PRD FR-09）。

`POST /v1/notes/{noteId}:expunge` → `204`，清除正文、页面对象、版本、标签关联与分享链接。

#### 5.3.10 `POST /v1/notes/{noteId}:appendPages`（P1）

请求 `{ "uploadIds": ["..."] }`，约束：笔记 `state=NORMAL`、单次 ≤20、累计 ≤50（PRD FR-18）。
响应 `202`：`{ "conversionJob": { ... } }`。追加结果只追加到正文末尾，不覆盖已有内容。

#### 5.3.11 `GET /v1/notes:search`（P1）

参数：`query`（1–100 字符）、`folderId`（含子树）、`tagIds`（可重复）、`pageSize`、`pageToken`。
响应：

```json
{
  "items": [
    {
      "noteId": "018f...",
      "title": "矩阵的秩",
      "snippet": "…矩阵的<em>秩</em>等于行阶梯形…",
      "folderPath": "我的笔记 / 数学 / 线性代数",
      "updatedAt": "2026-10-28T06:30:00Z"
    }
  ],
  "nextPageToken": "",
  "totalSize": 1
}
```

排序：标题命中优先于正文命中，同级按更新时间倒序（PRD FR-12）。`snippet` 中的 `<em>` 是唯一允许的标记，客户端按纯文本渲染时需剥离。

### 5.4 图片上传与页面

#### 5.4.1 `POST /v1/uploads`

请求：

```json
{
  "items": [
    { "fileName": "p1.jpg", "contentType": "image/jpeg", "bytes": 1048576, "sha256": "9f2c..." }
  ]
}
```

约束：单次 1–20 项；`contentType` ∈ `image/jpeg`、`image/png`、`image/webp`；`bytes` ≤ 10MB。

响应 `201`：

```json
{
  "items": [
    {
      "uploadId": "018f...",
      "objectKey": "u/018f/2026/10/018f.jpg",
      "putUrl": "https://minio.example/u/...?X-Amz-Signature=...",
      "headers": { "Content-Type": "image/jpeg" },
      "expiresAt": "2026-10-28T06:30:00Z"
    }
  ]
}
```

#### 5.4.2 `POST /v1/uploads/{uploadId}:complete`

请求 `{ "sha256": "9f2c..." }` → `200`：

```json
{
  "uploadId": "018f...",
  "status": "READY",
  "width": 1600,
  "height": 1200,
  "bytes": 1048576,
  "thumbUrl": "https://minio.example/thumb/...?X-Amz-Signature=..."
}
```

错误：`UPLOAD_EXPIRED`(410)、`UPLOAD_HASH_MISMATCH`(409)、`UPLOAD_SIZE_MISMATCH`(409)、`UPLOAD_UNSUPPORTED_TYPE`(415)、`UPLOAD_NOT_FOUND`(404)。

#### 5.4.3 `GET /v1/notes/{noteId}/pages`

响应 `200` 集合：

```json
{
  "items": [
    { "id": "018f...", "index": 1, "width": 1600, "height": 1200, "bytes": 1048576, "thumbUrl": "https://...", "createdAt": "2026-10-28T06:00:00Z" }
  ],
  "nextPageToken": ""
}
```

`index` 从 1 开始，即用户排序（PRD FR-03）。

#### 5.4.4 `GET /v1/notes/{noteId}/pages/{pageId}`

响应 `200`：

```json
{ "id": "018f...", "index": 1, "originalUrl": "https://minio.example/...?X-Amz-Signature=...", "expiresAt": "2026-10-28T06:10:00Z" }
```

`originalUrl` 有效期 10 分钟；客户端不得缓存地址超过有效期，需重新请求。

### 5.5 转换任务

#### 5.5.1 任务对象

```json
{
  "id": "018f...",
  "noteId": "018f...",
  "kind": "CREATE",
  "state": "RUNNING",
  "phase": "RECOGNIZING",
  "totalPages": 3,
  "processedPages": 1,
  "failureCode": null,
  "failureUserMessage": null,
  "quotaCharged": 3,
  "createdAt": "2026-10-28T06:00:00Z",
  "startedAt": "2026-10-28T06:00:02Z",
  "finishedAt": null
}
```

`GET /v1/notes/{noteId}/conversionJob` 返回上述对象；从未提交过转换时返回 `404 CONVERSION_NOT_FOUND`。
轮询建议间隔 2 秒；任务终态后客户端再拉取笔记详情（PRD FR-05）。

#### 5.5.2 `POST /v1/notes/{noteId}/conversionJob:cancel`

仅 `QUEUED`、`RUNNING` 可取消，成功 `200` 返回任务对象（`state=CANCELED`）；其他状态返回 `409 CONVERSION_STATE_INVALID`。取消后额度自动返还，页面图片保留。

#### 5.5.3 `POST /v1/notes/{noteId}/conversionJob:retry`

创建新任务并复用已有页面（PRD FR-05「重试不重新选图」），响应 `202` 返回新任务对象。前置条件：笔记 `state ∈ {DRAFT, NORMAL}` 且有页面；额度与容量重新校验。

### 5.6 回收站

`GET /v1/trash?pageSize=20&pageToken=...` → `200`

```json
{
  "items": [
    {
      "batchId": "018f...",
      "itemType": "FOLDER",
      "itemId": "018f...",
      "name": "数学",
      "originalPath": "我的笔记 / 数学",
      "folderCount": 4,
      "noteCount": 37,
      "deletedAt": "2026-10-28T06:00:00Z",
      "purgeAt": "2026-11-27T06:00:00Z",
      "remainingDays": 30
    }
  ],
  "nextPageToken": ""
}
```

恢复与彻底删除按类型调用对应资源的 `:undelete` / `:expunge`，回收站本身不提供写操作（PRD FR-09 单条操作）。

### 5.7 标签（P1）

`Tag`：`{ "id": "...", "name": "考研", "noteCount": 7, "createdAt": "..." }`。

- `GET /v1/tags`：按 `noteCount desc` 默认排序。
- `POST /v1/tags`：`{ "name": "考研" }`，1–20 字符、无首尾空格、账号内唯一；重名返回 `409 NAME_DUPLICATED`。
- `PATCH /v1/tags/{tagId}`：`{ "name": "..." }`，重命名后所有关联笔记同步展示新名称。
- `DELETE /v1/tags/{tagId}`：解除全部关联，不删除笔记（PRD FR-13）。
- `PUT /v1/notes/{noteId}/tags`：`{ "tagIds": ["..."] }`，整体覆盖；超过 10 个返回 `409 TAG_LIMIT_EXCEEDED`；不存在的 `tagId` 返回 `404`。

### 5.8 版本（P1）

- `GET /v1/notes/{noteId}/versions` → 集合 `{ versionId, source, charCount, createdAt, expiresAt }`。
- `GET /v1/notes/{noteId}/versions/{versionId}` → `{ versionId, content, charCount, createdAt }`。
- `POST /v1/notes/{noteId}/versions/{versionId}:restore` → `200` `NoteDetail`；生成新版本（`source=RESTORE`），不删除历史版本；只替换正文（PRD FR-16）。

### 5.9 分享（P1）

- `GET /v1/notes/{noteId}/shareLink` → `{ "enabled": true, "url": "https://{host}/s/{token}", "createdAt": "...", "updatedAt": "..." }`；从未开启过分享时返回 `200 { "enabled": false }`（不以 404 区分），只有笔记不存在才返回 `404`。
- `POST /v1/notes/{noteId}/shareLink` → `201` 同上；仅 `state=NORMAL` 可开启，否则 `409 SHARE_NOTE_NOT_READY`。
- `DELETE /v1/notes/{noteId}/shareLink` → `204`，立即失效。
- `GET /v1/public/shares/{token}`（无鉴权）→ `200`：

```json
{
  "title": "矩阵的秩",
  "content": "# 矩阵的秩\n\n...",
  "pages": [{ "index": 1, "originalUrl": "https://minio.example/...?X-Amz-Signature=..." }],
  "updatedAt": "2026-10-28T06:30:00Z"
}
```

失效、关闭、笔记被删除或其目录被删除时返回 `410 SHARE_LINK_INACTIVE`，响应体不含笔记标题与内容（PRD FR-15、5.7）。匿名端点限流 60 次/分钟/IP。

`GET /s/{token}`（无鉴权）→ `200 text/html`：与 `shareLink.url` 完全同一地址，服务端渲染只读页面，
正文按纯文本转义输出、页面原图懒加载；CSP 禁止脚本与框架，限流口径同 `GET /v1/public/shares/{token}`。
两个匿名端点均返回 `Cache-Control: no-store`，关闭分享后的 `410` 不会被中间缓存延迟。

### 5.10 增量同步

`GET /v1/sync:changes?since=0&pageSize=200&resourceTypes=NOTE&resourceTypes=FOLDER&resourceTypes=TAG`

| 参数 | 类型 | 必选 | 说明 |
|---|---|---|---|
| `since` | int64 | 否 | 上次响应中的 `nextCursor`；缺省 0 表示全量初始化 |
| `pageSize` | int32 | 否 | 默认 200，最大 500 |
| `resourceTypes` | enum[] | 否 | 缺省全部类型 |

响应 `200`：

```json
{
  "changes": [
    { "seq": 1025, "resourceType": "NOTE", "resourceId": "018f...", "op": "UPDATE", "revision": 9, "updatedAt": "2026-10-28T06:30:00Z", "payload": { "title": "矩阵的秩", "content": "..." } },
    { "seq": 1026, "resourceType": "NOTE", "resourceId": "018f...", "op": "DELETE", "updatedAt": "2026-10-28T06:31:00Z", "payload": null }
  ],
  "nextCursor": 1026,
  "hasMore": false
}
```

规则：

1. `nextCursor` 必须严格单调；客户端先落库再推进游标。
2. `op=DELETE` 为墓碑事件，`payload=null`，客户端据此清除本地缓存。
3. `hasMore=true` 时客户端应立即拉下一页，`pageSize` 保持不变。
4. `since` 超出可回溯范围（变更日志保留 90 天）返回 `410 SYNC_CURSOR_EXPIRED`，客户端需全量重新初始化。
5. 离线变更的提交不走该端点，而是按发生顺序重放普通写接口并携带 `Idempotency-Key`（PRD FR-10）。

### 5.11 导出与导入（P1）

- `POST /v1/exports`：`{ "noteId": "..." }` 或 `{ "folderId": "..." }` + `{ "format": "MARKDOWN_ZIP" | "PDF" }` → `202` `{ "exportId": "...", "state": "QUEUED" }`。
- `GET /v1/exports/{exportId}` → `{ "exportId", "state": "QUEUED|RUNNING|SUCCEEDED|FAILED", "downloadUrl", "expiresAt", "failureCode" }`；`downloadUrl` 有效期 1 小时。
- `POST /v1/imports`：`{ "folderId", "mode": "PER_IMAGE|MERGE", "uploadIds": [...] }`（≤50）→ `202` `{ "importId", "state" }`。
- `GET /v1/imports/{importId}` → `{ "importId", "state", "total", "succeeded", "failed", "items": [{ "uploadId", "noteId", "state", "failureCode" }] }`。

### 5.12 运维探针

- `GET /healthz` → `200 {"status":"ok"}`，仅检查进程存活，不访问依赖。
- `GET /readyz` → `200 {"status":"ok","env":"dev","checks":{"vision":"ok","mailer":"ok","objectStore":"ok"}}`，逐项检查已装配能力：`objectStore` 探测 MinIO 桶可达性，`vision`/`mailer` 为适配器健康状态（见 [ARCHITECTURE.md](ARCHITECTURE.md) 8.16）。
- 任一检查项失败返回 `503`，`status` 为 `unavailable`，失败项值统一为 `unavailable`（不返回底层错误文本，避免向匿名探针泄漏内部细节）。失败响应与成功响应同构，不使用 Problem 结构，便于运维脚本统一解析。
- 探针仅对回环地址开放。数据库与 Redis 适配器接入后，`checks` 追加 `postgres`、`redis` 键，属于向后兼容的新增字段（第 7 章）。

## 6. 错误码目录

`code` 全局唯一且与 HTTP 状态码绑定，新增只能追加（AIP-180）。`userMessage` 为产品可直接展示的文案模板，`{var}` 由 `metadata` 填充。

### 6.1 认证与账号

| code | HTTP | 场景 | userMessage |
|---|---|---|---|
| `AUTH_INVALID_CREDENTIALS` | 401 | 邮箱或密码错误 | 邮箱或密码不正确 |
| `AUTH_EMAIL_NOT_REGISTERED` | 404 | 邮箱未注册 | 该邮箱尚未注册 |
| `AUTH_EMAIL_ALREADY_EXISTS` | 409 | 邮箱已注册 | 该邮箱已注册，请直接登录 |
| `AUTH_CODE_INVALID` | 400 | 验证码错误 | 验证码不正确，请重新输入 |
| `AUTH_CODE_EXPIRED` | 400 | 验证码过期或作废 | 验证码已过期，请重新获取 |
| `AUTH_CODE_RATE_LIMITED` | 429 | 验证码发送过频 | 操作过于频繁，请 {retryAfterSeconds} 秒后重试 |
| `AUTH_TOO_MANY_ATTEMPTS` | 429 | 密码连续错误锁定 | 尝试次数过多，请 {retryAfterSeconds} 秒后重试 |
| `AUTH_TOKEN_EXPIRED` | 401 | Access Token 过期 | 登录已过期，请重新登录 |
| `AUTH_TOKEN_INVALID` | 401 | 令牌非法或已吊销 | 登录状态异常，请重新登录 |
| `AUTH_REFRESH_REUSED` | 401 | 刷新令牌被复用 | 登录状态异常，请重新登录 |
| `AUTH_AGREEMENT_REQUIRED` | 400 | 未勾选协议 | 请先阅读并同意用户协议与隐私政策 |

### 6.2 资源与校验

| code | HTTP | 场景 | userMessage |
|---|---|---|---|
| `VALIDATION_FAILED` | 400 | 参数格式或取值非法 | 输入内容有误，请检查后重试 |
| `RESOURCE_NOT_FOUND` | 404 | 不存在或不属于当前用户 | 内容不存在或已被删除 |
| `NAME_DUPLICATED` | 409 | 同级目录重名或标签重名 | 当前目录下已存在同名目录 |
| `REVISION_MISMATCH` | 412 | 目录/标签等非冲突型更新版本不符 | 内容已更新，请刷新后重试 |
| `FOLDER_DEPTH_EXCEEDED` | 409 | 超过 5 层 | 最多支持 5 层目录 |
| `FOLDER_CYCLE` | 409 | 移动到自身或子孙 | 不能移动到自身或其子目录 |
| `NOTE_TOO_LARGE` | 413 | 正文超过 20 万字符 | 正文已达上限，无法继续输入 |
| `NOTE_PAGE_LIMIT_EXCEEDED` | 409 | 单笔记页面超过 50 张 | 单篇笔记最多 50 张图片 |
| `NOTE_STATE_INVALID` | 409 | 当前状态不允许该操作 | 当前笔记状态不支持该操作 |
| `TAG_LIMIT_EXCEEDED` | 409 | 单笔记标签超过 10 个 | 每篇笔记最多 10 个标签 |
| `QUOTA_EXCEEDED` | 409 | 当日额度不足 | 今日额度剩余 {remaining} 张，可减少图片后重试 |
| `STORAGE_EXCEEDED` | 409 | 云端容量不足 | 云端空间不足，请清理后重试 |
| `PRECONDITION_FAILED` | 409 | 状态前置条件不满足（如彻底删除非删除态） | 当前状态不支持该操作 |

### 6.3 上传与转换

| code | HTTP | 场景 | userMessage |
|---|---|---|---|
| `UPLOAD_NOT_FOUND` | 404 | 上传会话不存在 | 图片上传记录不存在，请重新上传 |
| `UPLOAD_EXPIRED` | 410 | 会话超过 30 分钟 | 上传已超时，请重新上传 |
| `UPLOAD_NOT_READY` | 409 | 会话未完成校验 | 图片尚未上传完成 |
| `UPLOAD_SIZE_MISMATCH` | 409 | 实际字节与声明不符 | 图片上传不完整，请重试 |
| `UPLOAD_HASH_MISMATCH` | 409 | SHA256 不符 | 图片校验失败，请重新上传 |
| `UPLOAD_UNSUPPORTED_TYPE` | 415 | 格式不支持 | 仅支持 JPG、PNG、WEBP 格式 |
| `CONVERSION_NOT_FOUND` | 404 | 笔记无转换任务 | 暂无转换任务 |
| `CONVERSION_STATE_INVALID` | 409 | 当前任务状态不可取消/重试 | 当前转换状态不支持该操作 |
| `CONVERSION_FAILED` | 409 | 任务失败（详情见 metadata.failureCode） | 转换失败，请重试 |
| `PROVIDER_UNAVAILABLE` | 503 | 供应商不可用 | 当前使用人数较多，请稍后重试 |
| `IMAGE_UNREADABLE` | 422 | 图片无法识别 | 部分页面不清晰，建议重拍后重试 |
| `CONTENT_UNSUPPORTED` | 422 | 内容不支持转换 | 该内容暂不支持转换 |
| `CONVERSION_TIMEOUT` | 504 | 超过 5 分钟 | 转换超时，请重试 |
| `EMPTY_RESULT` | 422 | 未识别到文字 | 未能识别到可用的文字内容 |
| `NOTE_MODIFIED` | 409 | 转换期间用户修改了笔记，结算放弃写入 | 笔记在转换期间被修改，请重试 |
| `INTERNAL_ERROR` | 500 | 结算遇到数据库等基础设施故障 | 转换失败，请稍后重试 |

### 6.4 分享、同步与通用

| code | HTTP | 场景 | userMessage |
|---|---|---|---|
| `SHARE_LINK_INACTIVE` | 410 | 分享已关闭或笔记不可用 | 内容已失效 |
| `SHARE_NOTE_NOT_READY` | 409 | 草稿不可分享 | 请先完成转换再分享 |
| `SHARE_LINK_NOT_FOUND` | 404 | 保留：当前实现以 `enabled=false` 表达「未开启分享」 | 该笔记尚未开启分享 |
| `SYNC_CURSOR_EXPIRED` | 410 | 游标超出回溯范围 | 数据已更新，请重新同步 |
| `INVALID_PAGE_TOKEN` | 400 | 分页令牌过期或被篡改 | 列表已变化，请刷新后重试 |
| `IDEMPOTENCY_KEY_REUSED` | 409 | 同键不同请求体 | 操作冲突，请刷新后重试 |
| `RATE_LIMITED` | 429 | 触发限流 | 操作过于频繁，请稍后重试 |
| `INTERNAL` | 500 | 未预期错误 | 服务异常，请稍后重试 |
| `UNAVAILABLE` | 503 | 依赖不可用 | 服务暂时不可用，请稍后重试 |
| `TIMEOUT` | 504 | 上游超时 | 请求超时，请重试 |

## 7. 兼容与版本策略

1. **URL 版本**：破坏性变更升 `/v2`，`/v1` 至少并行 6 个月；废弃端点在响应头返回 `Deprecation` 与 `Sunset`。
2. **允许的兼容变更**：新增可选请求字段；新增响应字段；新增枚举值；新增错误码（客户端默认分支）；放宽校验；`pageSize` 默认值调整（不得降低上限）。
3. **禁止的兼容变更**：删除或重命名字段；收紧校验；改变字段类型；改变错误码与 HTTP 状态码的绑定；改变枚举既有值的语义。
4. **客户端容忍要求**：忽略未知字段与未知枚举；不依赖字段顺序；不依赖 `totalSize`；对 `nextPageToken` 为空即停止翻页。
5. **契约校验**：`openapi.yaml` 在 CI 中执行 lint（推荐 `redocly lint` 或 `spectral lint`），任何字段改动必须同步更新本文与契约，两者不一致视为评审阻塞项。

## 8. PRD 追溯

| PRD 条目 | 落地接口 |
|---|---|
| FR-01 账号与登录 | 1–9 |
| FR-02 目录管理 | 11–19 |
| FR-03 取图 / FR-04 上传 | 31、32、33、34 |
| FR-05 AI 转换 | 22、35、36、37 |
| FR-06 笔记编辑 | 23、24 |
| FR-07 列表与浏览 | 20、23 |
| FR-08 预览与原文 | 23、33、34 |
| FR-09 删除与回收站 | 16、19、25、28、38、17、18、27 |
| FR-10 云端同步 | 51，以及全部写接口的幂等与冲突约定 |
| FR-11 设置 | 7、8、10 |
| FR-12 全文搜索 | 30 |
| FR-13 标签 | 39–43 |
| FR-14 导出 | 52、53 |
| FR-15 分享 | 47–50、50a |
| FR-16 版本历史 | 44–46 |
| FR-17 批量导入 | 54、55 |
| FR-18 追加页面 | 29 |
| 5.3 额度 | 10、22，以及所有转换相关校验 |
| 5.3.1 云端存储 | 10、22、29、31 |
| 5.8 时间口径 | 2.1、5.1.7 的 `quotaDate` / `resetAt` |

## 9. 修订记录

| 版本 | 日期 | 修订人 | 说明 |
|---|---|---|---|
| v1.0 | 2026-10-28 | 服务端接口组 | 首版基线：55 个 `/v1` 业务端点、错误码目录、PRD 追溯 |
| v1.1 | 2026-11-01 | 服务端接口组 | 交付口径对齐：补充 `GET /s/{token}` 公开分享页（50a）；`JobFailureCode` 增加 `NOTE_MODIFIED`、`INTERNAL_ERROR`；两个匿名分享端点统一 `Cache-Control: no-store` |
