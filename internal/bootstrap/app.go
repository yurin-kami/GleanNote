/*
 * Package bootstrap 装配进程所需的适配器、传输层与释放钩子。
 *
 * 视觉识别可接入 deepseek，邮件等付费能力仍为开发态 Fake；非开发环境出现 Fake 必须启动失败。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
	"github.com/gleannote/gleannote/internal/config"
	"github.com/gleannote/gleannote/internal/infrastructure/codehash"
	"github.com/gleannote/gleannote/internal/infrastructure/imageprocessor"
	"github.com/gleannote/gleannote/internal/infrastructure/mail"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/infrastructure/password"
	"github.com/gleannote/gleannote/internal/infrastructure/pdfdoc"
	"github.com/gleannote/gleannote/internal/infrastructure/postgres"
	redisadapter "github.com/gleannote/gleannote/internal/infrastructure/redis"
	"github.com/gleannote/gleannote/internal/infrastructure/token"
	"github.com/gleannote/gleannote/internal/infrastructure/vision"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
	httptransport "github.com/gleannote/gleannote/internal/transport/http"
)

/* bootTimeout 限制装配期访问外部依赖的等待时间，避免容器未就绪时挂起 */
const bootTimeout = 15 * time.Second

/* App 是装配完成的进程依赖集合；字段一经创建即只读 */
type App struct {
	Router    http.Handler
	Providers Providers
	closers   []func() error
}

/* Providers 汇总已装配的外部能力，供探针与启动日志使用 */
type Providers struct {
	Vision      port.VisionProvider
	Mailer      port.Mailer
	ObjectStore port.ObjectStore
	Repository  string
	Cache       string
}

/*
 * New 按配置创建全部适配器并生成路由。
 * out 用于开发态邮件输出，通常传 os.Stdout；为 nil 时邮件不落终端。
 */
func New(cfg config.Config, out io.Writer, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if out == nil {
		out = io.Discard
	}

	/* 逐个装配能力；任一名称未实现立即失败，禁止静默降级为 Fake */
	visionProvider, err := vision.New(vision.Options{
		Name:    cfg.Providers.Vision,
		Latency: cfg.Providers.VisionLatency,
		Failure: cfg.Providers.VisionFailure,
		APIKey:  cfg.Vision.APIKey,
		BaseURL: cfg.Vision.BaseURL,
		Model:   cfg.Vision.Model,
		Timeout: cfg.Vision.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("装配视觉提供者: %w", err)
	}
	mailer, err := mail.New(cfg.Providers.Mailer, out, cfg.Providers.MailRevealCode)
	if err != nil {
		return nil, fmt.Errorf("装配邮件提供者: %w", err)
	}

	/* 对象存储需要网络校验桶，单独加超时；失败信息应提示本地容器是否已启动 */
	storeCtx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()
	store, err := objectstore.New(storeCtx, cfg.Providers.ObjectStore, objectstore.Options{
		Endpoint:  cfg.S3.Endpoint,
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		Bucket:    cfg.S3.Bucket,
		Region:    cfg.S3.Region,
		UseSSL:    cfg.S3.UseSSL,
	}, clock.System{})
	if err != nil {
		return nil, fmt.Errorf("装配对象存储: %w", err)
	}

	providers := Providers{
		Vision:      visionProvider,
		Mailer:      mailer,
		ObjectStore: store,
		Repository:  selectedRepository(cfg),
		Cache:       selectedCache(cfg),
	}
	if err := guardFakeInProd(cfg.Env, providers); err != nil {
		return nil, err
	}

	/*
	 * 持久化与缓存按配置选择：memory 供单元测试与离线开发，postgres/redis 为运行态。
	 * 无论选哪种实现，上层只依赖端口，业务代码不受影响。
	 */
	cache, err := newCacheLayer(storeCtx, cfg)
	if err != nil {
		return nil, err
	}
	data, err := newDataLayer(storeCtx, cfg)
	if err != nil {
		return nil, err
	}
	data.Queue = cache.Queue

	folderRepo := data.Folders
	noteRepo := data.Notes
	uploadRepo := data.Uploads
	pageRepo := data.Pages
	jobRepo := data.Jobs
	quotaLedger := data.Quota
	storageUsage := data.Storage
	conversionQueue := data.Queue
	tagRepo := data.Tags
	noteTagRepo := data.NoteTags
	versionRepo := data.Versions
	shareRepo := data.Shares
	exportJobRepo := data.Exports
	importJobRepo := data.Imports

	auth, err := newAuthComponents(cfg, mailer, data, cache)
	if err != nil {
		return nil, err
	}
	folders, err := newFolderService(cfg, data.Transactions, folderRepo, noteRepo, folderMediaDeps{
		Pages:    pageRepo,
		Jobs:     jobRepo,
		Tags:     noteTagRepo,
		Versions: versionRepo,
		Shares:   shareRepo,
		Storage:  storageUsage,
		Objects:  providers.ObjectStore,
	})
	if err != nil {
		return nil, err
	}
	versions, err := newVersionService(cfg, data.Transactions, versionRepo, noteRepo)
	if err != nil {
		return nil, err
	}
	shares, err := newShareService(cfg, noteRepo, folderRepo, pageRepo, providers.ObjectStore, shareRepo)
	if err != nil {
		return nil, err
	}
	notes, err := newNoteService(cfg, data.Transactions, noteRepo, folderRepo, folders, noteTagRepo,
		versions, shareRepo, mediaDeps{
			Uploads: uploadRepo,
			Pages:   pageRepo,
			Jobs:    jobRepo,
			Quota:   quotaLedger,
			Storage: storageUsage,
			Queue:   conversionQueue,
			Objects: providers.ObjectStore,
		})
	if err != nil {
		return nil, err
	}
	uploads, err := newUploadService(cfg, data.Transactions, uploadRepo, noteRepo, pageRepo, providers.ObjectStore)
	if err != nil {
		return nil, err
	}
	conversions, err := newConversionService(conversionWorkerDeps{
		Notes:        noteRepo,
		Pages:        pageRepo,
		Jobs:         jobRepo,
		Quota:        quotaLedger,
		Storage:      storageUsage,
		Queue:        conversionQueue,
		Vision:       providers.Vision,
		Objects:      providers.ObjectStore,
		Versions:     versions,
		Transactions: data.Transactions,
	})
	if err != nil {
		return nil, err
	}
	tags, err := newTagService(cfg, data.Transactions, tagRepo, noteTagRepo, noteRepo)
	if err != nil {
		return nil, err
	}
	trash, err := newTrashService(cfg, folderRepo, noteRepo)
	if err != nil {
		return nil, err
	}
	changes, err := newChangesService(data.Changes)
	if err != nil {
		return nil, err
	}
	exports, err := newExportService(providers.ObjectStore, exportJobRepo, noteRepo, folderRepo, pageRepo)
	if err != nil {
		return nil, err
	}
	imports, err := newImportService(folderRepo, importJobRepo, notes)
	if err != nil {
		return nil, err
	}

	/* 传输层只接收端口，不感知具体适配器实现 */
	router, err := httptransport.NewRouter(httptransport.Deps{
		Env:                cfg.Env,
		Logger:             logger,
		Clock:              clock.System{},
		Vision:             providers.Vision,
		Mailer:             providers.Mailer,
		ObjectStore:        providers.ObjectStore,
		Auth:               auth.Service,
		Folders:            folders,
		Notes:              notes,
		Uploads:            uploads,
		Conversions:        conversions,
		Tags:               tags,
		Trash:              trash,
		Versions:           versions,
		Shares:             shares,
		Changes:            changes,
		Exports:            exports,
		Imports:            imports,
		AccessTokens:       auth.Verifier,
		Denylist:           auth.Denylist,
		Idempotency:        auth.Idempotency,
		PublicShareLimiter: cache.PublicLimiter,
		Limiter:            cache.Limiter,
		Database:           data.Health,
		Cache:              cache.Health,
	})
	if err != nil {
		return nil, fmt.Errorf("装配路由: %w", err)
	}

	/*
	 * 释放顺序与登记顺序相反：先停 Worker，再关缓存连接，最后关数据库连接，
	 * 保证在途任务在连接释放前收尾。
	 */
	closers := make([]func() error, 0, 4)
	closers = append(closers, data.closers...)
	closers = append(closers, cache.closers...)

	/* GN_MODE=api 只提供 HTTP；all/worker 在本进程内消费转换队列并清理到期回收站 */
	if cfg.Mode != "api" {
		worker := startConversionWorker(conversions, conversionQueue, logger)
		closers = append(closers, worker.Close)
		logger.Info("转换 Worker 已启动", "concurrency", conversionWorkerCount)

		purger := startTrashPurger(folders, notes, logger)
		closers = append(closers, purger.Close)
		logger.Info("回收站到期清理已启动", "interval", purgeInterval, "batchLimit", purgeBatchLimit)
	}

	logger.Info("装配完成",
		"env", cfg.Env,
		"mode", cfg.Mode,
		"vision", providers.Vision.Name(),
		"mailer", providers.Mailer.Name(),
		"objectStore", providers.ObjectStore.Name(),
		"repository", providers.Repository,
		"cache", providers.Cache,
	)
	return &App{Router: router, Providers: providers, closers: closers}, nil
}

/*
 * dataLayer 汇总业务持久化端口与事务边界。
 * memory 与 postgres 两种实现产出同一组端口，上层用例无需感知具体适配器。
 */
type dataLayer struct {
	Transactions port.Transactor

	Users         port.UserRepository
	EmailCodes    port.EmailCodeRepository
	RefreshTokens port.RefreshTokenRepository
	Folders       port.FolderRepository
	Notes         port.NoteRepository
	Uploads       port.UploadRepository
	Pages         port.NotePageRepository
	Jobs          port.ConversionJobRepository
	Quota         port.QuotaLedger
	Storage       port.StorageUsage
	Tags          port.TagRepository
	NoteTags      port.NoteTagRepository
	Versions      port.NoteVersionRepository
	Shares        port.ShareLinkRepository
	Exports       port.ExportJobRepository
	Imports       port.ImportJobRepository
	Changes       port.ChangeLog

	/* Purge 供注销用例一次性清除账号全部业务数据 */
	Purge port.AccountDataPurger

	/* Queue 由缓存层注入，保证同一进程的入队与出队指向同一后端 */
	Queue port.ConversionQueue

	/* Health 供就绪探针追加连通性检查；memory 实现为空，探针不追加该项 */
	Health port.HealthChecker

	/* closers 在 App.Close 时逆序执行，用于释放连接池 */
	closers []func() error
}

/*
 * newDataLayer 按 GN_REPOSITORY_PROVIDER 装配业务仓储。
 * memory 供测试与离线开发；postgres 建池并应用内嵌迁移，连接或迁移失败必须在装配期暴露。
 */
func newDataLayer(ctx context.Context, cfg config.Config) (*dataLayer, error) {
	switch selectedRepository(cfg) {
	case "postgres":
		db, err := postgres.Open(ctx, cfg.Database.URL, cfg.Database.MaxConns)
		if err != nil {
			return nil, fmt.Errorf("装配 PostgreSQL: %w", err)
		}
		changes := postgres.NewChangeLogRepository(db)
		return &dataLayer{
			Transactions:  db,
			Users:         postgres.NewUserRepository(db),
			EmailCodes:    postgres.NewEmailCodeRepository(db),
			RefreshTokens: postgres.NewRefreshTokenRepository(db),
			Folders:       postgres.NewFolderRepository(db, changes),
			Notes:         postgres.NewNoteRepository(db, changes),
			Uploads:       postgres.NewUploadRepository(db),
			Pages:         postgres.NewNotePageRepository(db, changes),
			Jobs:          postgres.NewConversionJobRepository(db),
			Quota:         postgres.NewQuotaLedger(db),
			Storage:       postgres.NewStorageUsage(db),
			Tags:          postgres.NewTagRepository(db, changes),
			NoteTags:      postgres.NewNoteTagRepository(db, changes),
			Versions:      postgres.NewNoteVersionRepository(db),
			Shares:        postgres.NewShareLinkRepository(db),
			Exports:       postgres.NewExportJobRepository(db),
			Imports:       postgres.NewImportJobRepository(db),
			Changes:       changes,
			Purge:         postgres.NewAccountPurger(db),
			Health:        db,
			closers:       []func() error{db.Close},
		}, nil
	case "memory":
		store := memory.NewStore()
		changes := memory.NewChangeLogRepository(store)
		/* 绑定日志后，各仓储在写事务内自动追加同步事件 */
		store.SetChangeLog(changes)
		users := memory.NewUserRepository(store)
		codes := memory.NewEmailCodeRepository(store)
		refreshTokens := memory.NewRefreshTokenRepository(store)
		folders := memory.NewFolderRepository(store)
		notes := memory.NewNoteRepository(store)
		uploads := memory.NewUploadRepository(store)
		pages := memory.NewNotePageRepository(store)
		jobs := memory.NewConversionJobRepository(store)
		quota := memory.NewQuotaLedger(store)
		storage := memory.NewStorageUsage(store)
		tags := memory.NewTagRepository(store)
		noteTags := memory.NewNoteTagRepository(store, tags, notes)
		versions := memory.NewNoteVersionRepository(store)
		shares := memory.NewShareLinkRepository(store)
		exports := memory.NewExportJobRepository(store)
		imports := memory.NewImportJobRepository(store)
		/* 注销清理复用同一批仓储实例，保证与业务读写共享同一份内存数据 */
		purge := memory.NewAccountPurger(store, memory.AccountPurgerDeps{
			Users:         users,
			Codes:         codes,
			RefreshTokens: refreshTokens,
			Folders:       folders,
			Notes:         notes,
			Pages:         pages,
			Uploads:       uploads,
			Jobs:          jobs,
			Quota:         quota,
			Storage:       storage,
			Tags:          tags,
			NoteTags:      noteTags,
			Versions:      versions,
			Shares:        shares,
			Exports:       exports,
			Imports:       imports,
			Changes:       changes,
		})
		return &dataLayer{
			Transactions:  store,
			Users:         users,
			EmailCodes:    codes,
			RefreshTokens: refreshTokens,
			Folders:       folders,
			Notes:         notes,
			Uploads:       uploads,
			Pages:         pages,
			Jobs:          jobs,
			Quota:         quota,
			Storage:       storage,
			Tags:          tags,
			NoteTags:      noteTags,
			Versions:      versions,
			Shares:        shares,
			Exports:       exports,
			Imports:       imports,
			Changes:       changes,
			Purge:         purge,
		}, nil
	default:
		return nil, fmt.Errorf("未知持久化提供者: %q", cfg.Database.Repository)
	}
}

/*
 * cacheLayer 汇总限流、失败守卫、幂等、令牌黑名单与转换队列。
 * 进程内实现只适合单实例；redis 让多实例共享同一份安全状态与队列。
 */
type cacheLayer struct {
	Name          string
	CodeLimiter   port.RateLimiter
	PublicLimiter port.RateLimiter
	/* Limiter 承载全局/登录/写/上传四类限流，键前缀互不冲突 */
	Limiter       port.RateLimiter
	LoginAttempts port.FailureGuard
	Denylist      port.AccessTokenDenylist
	Idempotency   port.IdempotencyStore
	Queue         port.ConversionQueue

	/* Health 供就绪探针追加连通性检查；memory 实现为空 */
	Health port.HealthChecker

	closers []func() error
}

/*
 * newCacheLayer 按 GN_CACHE_PROVIDER 装配缓存与异步能力。
 * redis 连接失败必须终止启动，避免限流与幂等静默失效。
 */
func newCacheLayer(ctx context.Context, cfg config.Config) (*cacheLayer, error) {
	switch selectedCache(cfg) {
	case "redis":
		client, err := redisadapter.New(ctx, redisadapter.Options{URL: cfg.Redis.URL})
		if err != nil {
			return nil, fmt.Errorf("装配 Redis: %w", err)
		}
		return &cacheLayer{
			Name:          client.Name(),
			CodeLimiter:   redisadapter.NewRateLimiter(client),
			PublicLimiter: redisadapter.NewRateLimiter(client),
			Limiter:       redisadapter.NewRateLimiter(client),
			LoginAttempts: redisadapter.NewFailureGuard(client),
			Denylist:      redisadapter.NewAccessTokenDenylist(client),
			Idempotency:   redisadapter.NewIdempotencyStore(client),
			Queue:         redisadapter.NewConversionQueue(client),
			Health:        client,
			closers:       []func() error{client.Close},
		}, nil
	case "memory":
		now := clock.System{}
		queue := memory.NewConversionQueue(conversionQueueBuffer)
		return &cacheLayer{
			Name:          "memory",
			CodeLimiter:   memory.NewRateLimiter(now),
			PublicLimiter: memory.NewRateLimiter(now),
			Limiter:       memory.NewRateLimiter(now),
			LoginAttempts: memory.NewFailureGuard(now),
			Denylist:      memory.NewAccessTokenDenylist(now),
			Idempotency:   memory.NewIdempotencyStore(now),
			Queue:         queue,
			closers:       []func() error{func() error { queue.Close(); return nil }},
		}, nil
	default:
		return nil, fmt.Errorf("未知缓存提供者: %q", cfg.Redis.Provider)
	}
}

/* selectedRepository 归一化持久化提供者名称，空值按 memory 处理 */
func selectedRepository(cfg config.Config) string {
	name := strings.TrimSpace(strings.ToLower(cfg.Database.Repository))
	if name == "" {
		return "memory"
	}
	return name
}

/* selectedCache 归一化缓存提供者名称，空值按 memory 处理 */
func selectedCache(cfg config.Config) string {
	name := strings.TrimSpace(strings.ToLower(cfg.Redis.Provider))
	if name == "" {
		return "memory"
	}
	return name
}

/*
 * authComponents 汇总认证用例与传输层所需的配套端口。
 * 仓储来自 dataLayer，限流/幂等/黑名单来自 cacheLayer，二者可独立切换实现。
 */
type authComponents struct {
	Service     *authusecase.Service
	Verifier    port.AccessTokenVerifier
	Denylist    port.AccessTokenDenylist
	Idempotency port.IdempotencyStore
}

/*
 * newAuthComponents 按配置装配认证依赖。
 * 约束：HMAC 密钥与签名种子来自环境变量；种子为空时签发器生成一次性密钥（仅 dev）。
 * 仓储取自 dataLayer、安全能力取自 cacheLayer，保证与目录模块共享同一事务边界。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权
 */
func newAuthComponents(cfg config.Config, mailer port.Mailer, data *dataLayer,
	cache *cacheLayer) (*authComponents, error) {
	now := clock.System{}

	digester, err := codehash.New([]byte(cfg.Auth.HMACSecret))
	if err != nil {
		return nil, fmt.Errorf("装配验证码摘要器: %w", err)
	}
	signer, err := token.NewEd25519(cfg.Auth.SigningSeed)
	if err != nil {
		return nil, fmt.Errorf("装配访问令牌签发器: %w", err)
	}

	service, err := authusecase.NewService(authusecase.Deps{
		Clock:         now,
		IDs:           idgen.UUIDv7{},
		Users:         data.Users,
		Codes:         data.EmailCodes,
		RefreshTokens: data.RefreshTokens,
		Folders:       data.Folders,
		Account:       data.Purge,
		Transactions:  data.Transactions,
		Passwords:     password.NewArgon2id(),
		Digester:      digester,
		AccessTokens:  signer,
		Denylist:      cache.Denylist,
		CodeLimiter:   cache.CodeLimiter,
		LoginAttempts: cache.LoginAttempts,
		Mailer:        mailer,
	})
	if err != nil {
		return nil, fmt.Errorf("装配认证用例: %w", err)
	}
	return &authComponents{
		Service:     service,
		Verifier:    signer,
		Denylist:    cache.Denylist,
		Idempotency: cache.Idempotency,
	}, nil
}

/*
 * newFolderService 装配目录用例。
 * 分页令牌复用认证 HMAC 密钥派生签名，避免再引入一套密钥管理。
 * 媒体与附属数据端口用于目录级联彻底删除时的页面清理、容量释放与分享失效。
 */
func newFolderService(cfg config.Config, transactions port.Transactor,
	folders port.FolderRepository, notes folderusecase.NoteLinker,
	media folderMediaDeps) (*folderusecase.Service, error) {
	now := clock.System{}
	codec, err := folderusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配目录分页令牌: %w", err)
	}
	service, err := folderusecase.NewService(folderusecase.Deps{
		Clock:        now,
		IDs:          idgen.UUIDv7{},
		Folders:      folders,
		Notes:        notes,
		Pages:        media.Pages,
		Jobs:         media.Jobs,
		TagLinks:     media.Tags,
		Versions:     media.Versions,
		Shares:       media.Shares,
		Storage:      media.Storage,
		Objects:      media.Objects,
		Transactions: transactions,
		PageTokens:   codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配目录用例: %w", err)
	}
	return service, nil
}

/* folderMediaDeps 汇总目录级联删除所需的附属数据端口，避免参数列表过长 */
type folderMediaDeps struct {
	Pages    port.NotePageRepository
	Jobs     port.ConversionJobRepository
	Tags     folderusecase.NoteAttachmentCleaner
	Versions folderusecase.NoteAttachmentCleaner
	Shares   folderusecase.NoteAttachmentCleaner
	Storage  port.StorageUsage
	Objects  port.ObjectStore
}

/* mediaDeps 汇总媒体链路端口，避免 newNoteService 参数列表过长 */
type mediaDeps struct {
	Uploads port.UploadRepository
	Pages   port.NotePageRepository
	Jobs    port.ConversionJobRepository
	Quota   port.QuotaLedger
	Storage port.StorageUsage
	Queue   port.ConversionQueue
	Objects port.ObjectStore
}

/*
 * newNoteService 装配笔记用例。
 * 分页令牌同样复用认证 HMAC 密钥；目录恢复通过适配器复用目录用例，避免重复实现祖先恢复。
 * 媒体端口用于 createFromPages、页面摘要回填与彻底删除释放容量；版本与分享端口负责快照与链接失效。
 */
func newNoteService(cfg config.Config, transactions port.Transactor, notes port.NoteRepository,
	folders port.FolderRepository, folderService *folderusecase.Service,
	tagLinks noteusecase.TagLinks, versions noteusecase.VersionRecorder,
	shares noteusecase.ShareLinks, media mediaDeps) (*noteusecase.Service, error) {
	now := clock.System{}
	codec, err := noteusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配笔记分页令牌: %w", err)
	}
	service, err := noteusecase.NewService(noteusecase.Deps{
		Clock:          now,
		IDs:            idgen.UUIDv7{},
		Notes:          notes,
		Folders:        folders,
		FolderRestorer: folderRestorer{service: folderService},
		Uploads:        media.Uploads,
		Pages:          media.Pages,
		Jobs:           media.Jobs,
		Quota:          media.Quota,
		Storage:        media.Storage,
		Queue:          media.Queue,
		Objects:        media.Objects,
		TagLinks:       tagLinks,
		Versions:       versions,
		Shares:         shares,
		Transactions:   transactions,
		PageTokens:     codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配笔记用例: %w", err)
	}
	return service, nil
}

/*
 * newUploadService 装配上传与页面读取用例。
 * 图片处理为进程内实现；分页令牌与笔记模块使用同一套 HMAC 密钥派生规则。
 */
func newUploadService(cfg config.Config, transactions port.Transactor, uploads port.UploadRepository,
	notes port.NoteRepository, pages port.NotePageRepository,
	objects port.ObjectStore) (*uploadusecase.Service, error) {
	now := clock.System{}
	codec, err := uploadusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配页分页令牌: %w", err)
	}
	service, err := uploadusecase.NewService(uploadusecase.Deps{
		Clock:        now,
		IDs:          idgen.UUIDv7{},
		Uploads:      uploads,
		Notes:        notes,
		Pages:        pages,
		Objects:      objects,
		Images:       imageprocessor.New(),
		Transactions: transactions,
		PageTokens:   codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配上传用例: %w", err)
	}
	return service, nil
}

/* conversionWorkerDeps 汇总转换用例端口；Transactions 界定状态迁移的事务边界 */
type conversionWorkerDeps struct {
	Notes        port.NoteRepository
	Pages        port.NotePageRepository
	Jobs         port.ConversionJobRepository
	Quota        port.QuotaLedger
	Storage      port.StorageUsage
	Queue        port.ConversionQueue
	Vision       port.VisionProvider
	Objects      port.ObjectStore
	Versions     conversionusecase.VersionRecorder
	Transactions port.Transactor
}

/*
 * newConversionService 装配转换用例。
 * HTTP 的查询/取消/重试与 Worker 的 RunJob 共用同一实例，保证状态机口径一致。
 */
func newConversionService(deps conversionWorkerDeps) (*conversionusecase.Service, error) {
	service, err := conversionusecase.NewService(conversionusecase.Deps{
		Clock:        clock.System{},
		IDs:          idgen.UUIDv7{},
		Notes:        deps.Notes,
		Pages:        deps.Pages,
		Jobs:         deps.Jobs,
		Quota:        deps.Quota,
		Storage:      deps.Storage,
		Queue:        deps.Queue,
		Vision:       deps.Vision,
		Objects:      deps.Objects,
		Versions:     deps.Versions,
		Transactions: deps.Transactions,
	})
	if err != nil {
		return nil, fmt.Errorf("装配转换用例: %w", err)
	}
	return service, nil
}

/*
 * newVersionService 装配版本用例。
 * 分页令牌复用认证 HMAC 密钥；版本快照与正文共享同一事务边界。
 */
func newVersionService(cfg config.Config, transactions port.Transactor,
	versions port.NoteVersionRepository, notes port.NoteRepository) (*versionusecase.Service, error) {
	now := clock.System{}
	codec, err := versionusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配版本分页令牌: %w", err)
	}
	service, err := versionusecase.NewService(versionusecase.Deps{
		Clock:        now,
		IDs:          idgen.UUIDv7{},
		Versions:     versions,
		Notes:        notes,
		Transactions: transactions,
		PageTokens:   codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配版本用例: %w", err)
	}
	return service, nil
}

/*
 * newShareService 装配分享用例。
 * 公开地址基址来自配置；页面原图地址由对象存储签发，有效期 10 分钟。
 */
func newShareService(cfg config.Config, notes port.NoteRepository, folders port.FolderRepository,
	pages port.NotePageRepository, objects port.ObjectStore,
	shares port.ShareLinkRepository) (*shareusecase.Service, error) {
	service, err := shareusecase.NewService(shareusecase.Deps{
		Clock:   clock.System{},
		Notes:   notes,
		Folders: folders,
		Pages:   pages,
		Objects: objects,
		Shares:  shares,
		BaseURL: cfg.PublicBaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("装配分享用例: %w", err)
	}
	return service, nil
}

/*
 * newTagService 装配标签用例。
 * 标签本体与关联共享存储与事务边界；分页令牌复用认证 HMAC 密钥。
 */
func newTagService(cfg config.Config, transactions port.Transactor,
	tags port.TagRepository, noteTags port.NoteTagRepository,
	notes port.NoteRepository) (*tagusecase.Service, error) {
	now := clock.System{}
	codec, err := tagusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配标签分页令牌: %w", err)
	}
	service, err := tagusecase.NewService(tagusecase.Deps{
		Clock:        now,
		IDs:          idgen.UUIDv7{},
		Tags:         tags,
		NoteTags:     noteTags,
		Notes:        notes,
		Transactions: transactions,
		PageTokens:   codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配标签用例: %w", err)
	}
	return service, nil
}

/* newTrashService 装配回收站用例；回收站只读，因此不需要事务端口 */
func newTrashService(cfg config.Config, folders port.FolderRepository,
	notes port.NoteRepository) (*trashusecase.Service, error) {
	now := clock.System{}
	codec, err := trashusecase.NewPageTokenCodec([]byte(cfg.Auth.HMACSecret), now)
	if err != nil {
		return nil, fmt.Errorf("装配回收站分页令牌: %w", err)
	}
	service, err := trashusecase.NewService(trashusecase.Deps{
		Clock:      now,
		Folders:    folders,
		Notes:      notes,
		PageTokens: codec,
	})
	if err != nil {
		return nil, fmt.Errorf("装配回收站用例: %w", err)
	}
	return service, nil
}

/* newChangesService 装配增量同步用例；变更日志随业务写入由仓储同事务追加 */
func newChangesService(log port.ChangeLog) (*changesusecase.Service, error) {
	service, err := changesusecase.NewService(changesusecase.Deps{
		Clock: clock.System{},
		Log:   log,
	})
	if err != nil {
		return nil, fmt.Errorf("装配增量同步用例: %w", err)
	}
	return service, nil
}

/*
 * newExportService 装配导出用例。
 * 归档在请求内同步生成并写入对象存储，下载地址有效期 1 小时；
 * PDF 由 pdfdoc 渲染，中文使用 Type0/CJK 字体，页面图片以 JPEG 直接嵌入。
 */
func newExportService(objects port.ObjectStore, jobs port.ExportJobRepository, notes port.NoteRepository,
	folders port.FolderRepository, pages port.NotePageRepository) (*exportusecase.Service, error) {
	service, err := exportusecase.NewService(exportusecase.Deps{
		Clock:   clock.System{},
		IDs:     idgen.UUIDv7{},
		Notes:   notes,
		Folders: folders,
		Pages:   pages,
		Objects: objects,
		Jobs:    jobs,
		PDF:     pdfdoc.New(),
	})
	if err != nil {
		return nil, fmt.Errorf("装配导出用例: %w", err)
	}
	return service, nil
}

/* newImportService 装配批量导入用例；笔记创建复用笔记用例的 createFromPages */
func newImportService(folders port.FolderRepository, jobs port.ImportJobRepository,
	notes *noteusecase.Service) (*importsusecase.Service, error) {
	service, err := importsusecase.NewService(importsusecase.Deps{
		Clock:       clock.System{},
		IDs:         idgen.UUIDv7{},
		Folders:     folders,
		Jobs:        jobs,
		NoteCreator: notes,
	})
	if err != nil {
		return nil, fmt.Errorf("装配导入用例: %w", err)
	}
	return service, nil
}

/* folderRestorer 把目录恢复用例适配为笔记模块所需的最小接口 */
type folderRestorer struct {
	service *folderusecase.Service
}

/* RestoreNoteFolder 恢复笔记所在目录，并把结果收敛为笔记用例的返回类型 */
func (r folderRestorer) RestoreNoteFolder(ctx context.Context, userID, folderID string) (noteusecase.FolderRestore, error) {
	view, err := r.service.RestoreFolder(ctx, userID, folderID)
	if err != nil {
		return noteusecase.FolderRestore{}, err
	}
	return noteusecase.FolderRestore{FolderID: view.ID, RestoredToRoot: view.RestoredToRoot}, nil
}

/*
 * guardFakeInProd 阻止 staging/prod 使用开发态实现，避免预发验证失去意义。
 * 该校验放在装配期而非请求期，避免故障演练开关被误带到线上。
 */
func guardFakeInProd(env string, providers Providers) error {
	if env == "dev" {
		return nil
	}
	names := map[string]string{
		"vision":      providers.Vision.Name(),
		"mailer":      providers.Mailer.Name(),
		"objectStore": providers.ObjectStore.Name(),
	}
	for field, name := range names {
		if name == "fake" {
			return fmt.Errorf("%w: 非开发环境禁止使用 fake 提供者（%s）", port.ErrProviderNotConfigured, field)
		}
	}
	return nil
}

/*
 * Close 逆序执行释放钩子并合并错误。
 * 转换 Worker 的停止钩子会取消上下文并等待在途任务收尾，避免进程退出时丢弃已领取任务。
 */
func (a *App) Close() error {
	var merged error
	for index := len(a.closers) - 1; index >= 0; index-- {
		if err := a.closers[index](); err != nil {
			merged = errors.Join(merged, err)
		}
	}
	return merged
}
