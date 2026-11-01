/*
 * Package httptransport 是 HTTP 传输层：路由注册、中间件、处理器与运维探针。
 *
 * 业务路由按 docs/API.md 第 4、5 章逐步补齐；当前实现认证、目录、笔记、媒体链路，
 * 以及回收站、搜索与标签端点。
 * 参考：docs/API.md#41-账号与认证、docs/API.md#512-运维探针
 */
package httptransport

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/gleannote/gleannote/internal/application/port"
	authusecase "github.com/gleannote/gleannote/internal/application/usecase/auth"
	changesusecase "github.com/gleannote/gleannote/internal/application/usecase/changes"
	conversionusecase "github.com/gleannote/gleannote/internal/application/usecase/conversion"
	exportusecase "github.com/gleannote/gleannote/internal/application/usecase/export"
	folderusecase "github.com/gleannote/gleannote/internal/application/usecase/folder"
	importsusecase "github.com/gleannote/gleannote/internal/application/usecase/imports"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	shareusecase "github.com/gleannote/gleannote/internal/application/usecase/share"
	tagusecase "github.com/gleannote/gleannote/internal/application/usecase/tag"
	trashusecase "github.com/gleannote/gleannote/internal/application/usecase/trash"
	uploadusecase "github.com/gleannote/gleannote/internal/application/usecase/upload"
	versionusecase "github.com/gleannote/gleannote/internal/application/usecase/version"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* Deps 是路由所需的只读依赖，让传输层不感知装配细节 */
type Deps struct {
	Env                string
	Logger             *slog.Logger
	Clock              clock.Clock
	Vision             port.VisionProvider
	Mailer             port.Mailer
	ObjectStore        port.ObjectStore
	Auth               *authusecase.Service
	Folders            *folderusecase.Service
	Notes              *noteusecase.Service
	Uploads            *uploadusecase.Service
	Conversions        *conversionusecase.Service
	Tags               *tagusecase.Service
	Trash              *trashusecase.Service
	Versions           *versionusecase.Service
	Shares             *shareusecase.Service
	Changes            *changesusecase.Service
	Exports            *exportusecase.Service
	Imports            *importsusecase.Service
	AccessTokens       port.AccessTokenVerifier
	Denylist           port.AccessTokenDenylist
	Idempotency        port.IdempotencyStore
	PublicShareLimiter port.RateLimiter
	/* Limiter 用于全局、登录、写与上传四类限流；各类限流键前缀互不相同 */
	Limiter port.RateLimiter
	/* Database 与 Cache 为可选探针，memory 实现不提供时探针不追加对应字段 */
	Database port.HealthChecker
	Cache    port.HealthChecker
}

/* readyResponse 是就绪探针的响应体；字段追加属于向后兼容变更 */
type readyResponse struct {
	Status string            `json:"status"`
	Env    string            `json:"env"`
	Checks map[string]string `json:"checks"`
}

/*
 * NewRouter 装配中间件与全部已实现路由。
 * 返回错误表示装配缺失依赖；调用方必须终止启动，不允许带病运行。
 */
func NewRouter(deps Deps) (http.Handler, error) {
	if err := validateDeps(deps); err != nil {
		return nil, err
	}

	/* 生产与预发强制 release 模式，避免调试输出与路由表外泄 */
	if deps.Env == "prod" || deps.Env == "staging" {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	/*
	 * 不信任任何代理头：本服务直接对外监听，X-Forwarded-For 可被任意伪造。
	 * 若前置 Nginx/SLB，应改为显式配置可信网段，否则按 IP 的限流键可被绕过。
	 */
	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("设置可信代理: %w", err)
	}
	engine.Use(
		securityHeaders(),
		requestIDMiddleware(deps.Clock),
		accessLog(deps.Logger, deps.Clock),
		recoveryMiddleware(deps.Logger),
		globalRateLimit(deps.Limiter),
	)
	engine.GET("/healthz", handleHealth)
	engine.GET("/readyz", handleReady(deps))

	/* 路由表使用改写后的路径；对外仍是冒号动词，见 custom_method.go */
	guarded := authenticate(deps.AccessTokens, deps.Denylist, deps.Clock)
	h := &handler{
		auth:        deps.Auth,
		folders:     deps.Folders,
		notes:       deps.Notes,
		uploads:     deps.Uploads,
		conversions: deps.Conversions,
		tags:        deps.Tags,
		trash:       deps.Trash,
		versions:    deps.Versions,
		shares:      deps.Shares,
		changes:     deps.Changes,
		exports:     deps.Exports,
		imports:     deps.Imports,
	}
	registerAPIRoutes(engine.Group("/v1"), h, deps, guarded)

	/* 公开分享页：对外地址 {base}/s/{token}，服务端渲染只读页面，匿名访问仅按 IP 限流 */
	engine.GET("/s/:token", publicShareRateLimit(deps.PublicShareLimiter), h.publicSharePage)
	return rewriteCustomMethods(engine), nil
}

/* validateDeps 拒绝缺失依赖，避免运行期空指针；探针与业务路由都需要完整装配 */
func validateDeps(deps Deps) error {
	required := []struct {
		name string
		set  bool
	}{
		{"Logger", deps.Logger != nil},
		{"Clock", deps.Clock != nil},
		{"Vision", deps.Vision != nil},
		{"Mailer", deps.Mailer != nil},
		{"ObjectStore", deps.ObjectStore != nil},
		{"Auth", deps.Auth != nil},
		{"Folders", deps.Folders != nil},
		{"Notes", deps.Notes != nil},
		{"Uploads", deps.Uploads != nil},
		{"Conversions", deps.Conversions != nil},
		{"Tags", deps.Tags != nil},
		{"Trash", deps.Trash != nil},
		{"Versions", deps.Versions != nil},
		{"Shares", deps.Shares != nil},
		{"Changes", deps.Changes != nil},
		{"Exports", deps.Exports != nil},
		{"Imports", deps.Imports != nil},
		{"AccessTokens", deps.AccessTokens != nil},
		{"Denylist", deps.Denylist != nil},
		{"Idempotency", deps.Idempotency != nil},
		{"PublicShareLimiter", deps.PublicShareLimiter != nil},
		{"Limiter", deps.Limiter != nil},
	}

	/* 收集全部缺失项一次性返回，便于装配期一次看清所有问题 */
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("HTTP 路由缺少依赖: %v", missing)
	}
	return nil
}

/*
 * registerAPIRoutes 按 docs/API.md 第 4 章注册已实现端点。
 * 路径是改写后的形式：/auth:sendCode 在路由表中写作 /auth/sendCode。
 */
func registerAPIRoutes(v1 *gin.RouterGroup, h *handler, deps Deps, guarded gin.HandlerFunc) {
	/* 登录态路由统一叠加写限流：只读方法在中间件内部跳过，不消耗写配额 */
	protected := v1.Group("")
	protected.Use(guarded, writeRateLimit(deps.Limiter))

	/* 4.1 账号与认证：登录额外按 IP 限流，发码与注册保持公开 */
	v1.POST("/auth/sendCode", h.sendCode)
	v1.POST("/auth/register", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "auth:register"), h.register)
	v1.POST("/auth/login", loginRateLimit(deps.Limiter), h.login)
	v1.POST("/auth/refresh", h.refresh)
	protected.POST("/auth/logout", h.logout)
	v1.POST("/auth/resetPassword", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "auth:resetPassword"), h.resetPassword)
	protected.GET("/users/me", h.getMe)
	protected.PATCH("/users/me", h.updateMe)
	protected.DELETE("/users/me", h.deleteMe)
	protected.GET("/users/me/quota", h.getQuota)

	/* 4.2 目录：冒号方法在路由表写作 /folders/{folderId}/move 等 */
	protected.GET("/folders", h.listFolders)
	protected.POST("/folders", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "folder:create"), h.createFolder)
	protected.GET("/folders/:folderId", h.getFolder)
	protected.GET("/folders/:folderId/path", h.folderPath)
	protected.PATCH("/folders/:folderId", h.renameFolder)
	protected.DELETE("/folders/:folderId", h.deleteFolder)
	protected.POST("/folders/:folderId/move", h.moveFolder)
	protected.POST("/folders/:folderId/undelete", h.undeleteFolder)
	protected.POST("/folders/:folderId/expunge", h.expungeFolder)

	/* 4.3 笔记：冒号方法同样在路由表写作 /notes/{noteId}/move 等 */
	protected.GET("/notes", h.listNotes)
	protected.POST("/notes", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "note:create"), h.createNote)
	protected.GET("/notes/:noteId", h.getNote)
	protected.PATCH("/notes/:noteId", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "note:update"), h.updateNote)
	protected.DELETE("/notes/:noteId", h.deleteNote)
	protected.POST("/notes/:noteId/move", h.moveNote)
	protected.POST("/notes/:noteId/undelete", h.undeleteNote)
	protected.POST("/notes/:noteId/expunge", h.expungeNote)
	protected.POST("/notes/:noteId/appendPages",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "note:appendPages"), h.appendPages)

	/* 集合级自定义方法经 /_custom 改写注册，避开与 /notes/:noteId 的冲突 */
	protected.POST("/_custom/notes/createFromPages",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "note:createFromPages"), h.createFromPages)

	/* 4.4 图片上传、页面与转换任务：上传会话额外按小时限流 */
	protected.POST("/uploads", uploadRateLimit(deps.Limiter), h.createUploads)
	protected.POST("/uploads/:uploadId/complete", h.completeUpload)
	protected.GET("/notes/:noteId/pages", h.listPages)
	protected.GET("/notes/:noteId/pages/:pageId", h.getPage)
	protected.GET("/notes/:noteId/conversionJob", h.getConversionJob)
	protected.POST("/notes/:noteId/conversionJob/cancel", h.cancelConversionJob)
	protected.POST("/notes/:noteId/conversionJob/retry",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "note:retryConversion"), h.retryConversionJob)

	/* 5.3.11 搜索：集合级自定义方法，同样经 /_custom 改写 */
	protected.GET("/_custom/notes/search", h.searchNotes)

	/* 5.6 回收站：只读聚合视图 */
	protected.GET("/trash", h.listTrash)

	/* 5.7 标签 */
	protected.GET("/tags", h.listTags)
	protected.POST("/tags", idempotent(deps.Idempotency, deps.Clock, deps.Logger, "tag:create"), h.createTag)
	protected.PATCH("/tags/:tagId", h.renameTag)
	protected.DELETE("/tags/:tagId", h.deleteTag)
	protected.PUT("/notes/:noteId/tags", h.setNoteTags)

	/* 5.8 版本：列表、详情与恢复；恢复会改写正文，因此需要幂等保护 */
	protected.GET("/notes/:noteId/versions", h.listVersions)
	protected.GET("/notes/:noteId/versions/:versionId", h.getVersion)
	protected.POST("/notes/:noteId/versions/:versionId/restore",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "version:restore"), h.restoreVersion)

	/* 5.9 分享：开启与关闭；匿名读取不加鉴权，仅按 IP 限流 */
	protected.GET("/notes/:noteId/shareLink", h.getShareLink)
	protected.POST("/notes/:noteId/shareLink",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "share:create"), h.enableShare)
	protected.DELETE("/notes/:noteId/shareLink", h.disableShare)
	v1.GET("/public/shares/:token", publicShareRateLimit(deps.PublicShareLimiter), h.publicShare)

	/* 5.10 增量同步：对外 /v1/sync:changes，路由表写作 /v1/sync/changes */
	protected.GET("/sync/changes", h.listSyncChanges)

	/* 5.11 导出与导入：均为任务式端点，POST 需要幂等保护 */
	protected.POST("/exports",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "export:create"), h.createExport)
	protected.GET("/exports/:exportId", h.getExport)
	protected.POST("/imports",
		idempotent(deps.Idempotency, deps.Clock, deps.Logger, "import:create"), h.createImport)
	protected.GET("/imports/:importId", h.getImport)
}

/*
 * handleHealth 只证明进程可响应，不访问任何依赖。
 * 存活探针失败会触发重启，因此不得把外部依赖抖动计入。
 */
func handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

/*
 * handleReady 汇总已装配的付费能力探针。
 * 任一能力不可用即返回 503，并保留各项原因供运维定位。
 */
func handleReady(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		checks := map[string]string{
			"vision":      checkHealth(ctx, deps.Logger, "vision", deps.Vision.Health),
			"mailer":      checkHealth(ctx, deps.Logger, "mailer", deps.Mailer.Health),
			"objectStore": checkHealth(ctx, deps.Logger, "objectStore", deps.ObjectStore.Health),
		}
		/* 真实依赖接入后追加连通性检查；memory 实现不参与探针 */
		if deps.Database != nil {
			checks[deps.Database.Name()] = checkHealth(ctx, deps.Logger, deps.Database.Name(), deps.Database.Ping)
		}
		if deps.Cache != nil {
			checks[deps.Cache.Name()] = checkHealth(ctx, deps.Logger, deps.Cache.Name(), deps.Cache.Ping)
		}

		/* 任一项非 ok 即整体不可就绪；顺序无关，先命中即判定 */
		status := http.StatusOK
		overall := "ok"
		for _, value := range checks {
			if value != "ok" {
				status = http.StatusServiceUnavailable
				overall = "unavailable"
				break
			}
		}
		c.JSON(status, readyResponse{Status: overall, Env: deps.Env, Checks: checks})
	}
}

/*
 * checkHealth 统一探针返回口径：成功为 ok，失败只回固定文本。
 * 匿名探针不得回显原始错误（可能含主机名、库名与内网地址），细节仅写入服务端日志。
 */
func checkHealth(ctx context.Context, logger *slog.Logger, name string, probe func(context.Context) error) string {
	if err := probe(ctx); err != nil {
		if logger != nil {
			logger.Warn("依赖健康检查失败", "check", name, "error", err)
		}
		return "unavailable"
	}
	return "ok"
}
