/* HTTP 测试夹具：真实认证用例 + 内存仓储 + 可预测的哈希与 ID */
package httptransport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/infrastructure/codehash"
	"github.com/gleannote/gleannote/internal/infrastructure/imageprocessor"
	"github.com/gleannote/gleannote/internal/infrastructure/mail"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/infrastructure/pdfdoc"
	"github.com/gleannote/gleannote/internal/infrastructure/token"
	"github.com/gleannote/gleannote/internal/infrastructure/vision"
	"github.com/gleannote/gleannote/internal/platform/clock"
	httptransport "github.com/gleannote/gleannote/internal/transport/http"
)

/* plainHasher 用可预测编码替代 Argon2id，让 HTTP 测试聚焦协议而非哈希成本 */
type plainHasher struct{}

/* Hash 返回带前缀的明文编码 */
func (plainHasher) Hash(ctx context.Context, plain string) (string, error) {
	return "plain:" + plain, nil
}

/* Verify 只认本替身生成的编码 */
func (plainHasher) Verify(ctx context.Context, encoded, plain string) (bool, error) {
	return encoded == "plain:"+plain, nil
}

/* stubMailer 记录投递以便断言验证码确实发出，同时不访问网络 */
type stubMailer struct {
	sent []port.MailMessage
}

/* Name 返回替身名称 */
func (*stubMailer) Name() string { return "stub" }

/* Send 记录并立即成功 */
func (m *stubMailer) Send(ctx context.Context, msg port.MailMessage) error {
	m.sent = append(m.sent, msg)
	return nil
}

/* Health 恒为健康 */
func (*stubMailer) Health(ctx context.Context) error { return nil }

/* sequenceIDs 生成可读的自增 ID，便于断言与排查 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* testApp 汇总一次 HTTP 测试所需的全部依赖与固定时钟 */
type testApp struct {
	handler  http.Handler
	deps     httptransport.Deps
	clock    *clock.Fixed
	issuer   *token.Ed25519
	users    *memory.UserRepository
	codes    *memory.EmailCodeRepository
	folders  *memory.FolderRepository
	notes    *memory.NoteRepository
	tags     *memory.TagRepository
	uploads  *memory.UploadRepository
	pages    *memory.NotePageRepository
	jobs     *memory.ConversionJobRepository
	ledger   *memory.QuotaLedger
	objects  *objectstore.Fake
	mail     *stubMailer
	digester *codehash.Hasher
}

/* testFolderRestorer 复用真实目录用例恢复笔记所在目录，避免在测试内重复目录规则 */
type testFolderRestorer struct{ service *folderusecase.Service }

/* RestoreNoteFolder 把目录恢复结果裁剪成笔记用例需要的最小结构 */
func (r testFolderRestorer) RestoreNoteFolder(ctx context.Context, userID, folderID string) (noteusecase.FolderRestore, error) {
	view, err := r.service.RestoreFolder(ctx, userID, folderID)
	if err != nil {
		return noteusecase.FolderRestore{}, err
	}
	return noteusecase.FolderRestore{FolderID: view.ID, RestoredToRoot: view.RestoredToRoot}, nil
}

/* newTestApp 装配完整路由；时间固定为 2026-10-28T06:00:00Z 以便断言有效期 */
func newTestApp(t *testing.T) *testApp {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	/* 变更日志与仓储共享存储，HTTP 同步端点读取同一份事件 */
	changeLogRepo := memory.NewChangeLogRepository(store)
	store.SetChangeLog(changeLogRepo)

	digester, err := codehash.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("创建验证码摘要器: %v", err)
	}
	issuer, err := token.NewEd25519(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	if err != nil {
		t.Fatalf("创建令牌签发器: %v", err)
	}

	users := memory.NewUserRepository(store)
	codes := memory.NewEmailCodeRepository(store)
	folderRepo := memory.NewFolderRepository(store)
	noteRepo := memory.NewNoteRepository(store)
	tagRepo := memory.NewTagRepository(store)
	noteTagRepo := memory.NewNoteTagRepository(store, tagRepo, noteRepo)
	denylist := memory.NewAccessTokenDenylist(fixed)
	mailer := &stubMailer{}

	/*
	 * 笔记、上传、转换、版本与分享用例共享同一存储：
	 * 目录用例需要页面/任务/版本/分享/容量/对象存储来执行级联彻底删除，
	 * 因此这些仓储必须先于目录服务构造。
	 */
	uploadRepo := memory.NewUploadRepository(store)
	pageRepo := memory.NewNotePageRepository(store)
	jobRepo := memory.NewConversionJobRepository(store)
	ledger := memory.NewQuotaLedger(store)
	usage := memory.NewStorageUsage(store)
	queue := memory.NewConversionQueue(16)
	objects := objectstore.NewFake(fixed)
	visionProvider := vision.NewFake(0, "")
	versionRepo := memory.NewNoteVersionRepository(store)
	shareRepo := memory.NewShareLinkRepository(store)
	refreshRepo := memory.NewRefreshTokenRepository(store)
	exportJobRepo := memory.NewExportJobRepository(store)
	importJobRepo := memory.NewImportJobRepository(store)

	/* 注销清理复用同一批仓储实例，保证 HTTP 层验证的是完整级联删除 */
	purger := memory.NewAccountPurger(store, memory.AccountPurgerDeps{
		Users:         users,
		Codes:         codes,
		RefreshTokens: refreshRepo,
		Folders:       folderRepo,
		Notes:         noteRepo,
		Pages:         pageRepo,
		Uploads:       uploadRepo,
		Jobs:          jobRepo,
		Quota:         ledger,
		Storage:       usage,
		Tags:          tagRepo,
		NoteTags:      noteTagRepo,
		Versions:      versionRepo,
		Shares:        shareRepo,
		Exports:       exportJobRepo,
		Imports:       importJobRepo,
		Changes:       changeLogRepo,
	})

	/* 认证用例依赖真实内存仓储，保证 HTTP 层验证的是串联后的完整行为 */
	service, err := authusecase.NewService(authusecase.Deps{
		Clock:         fixed,
		IDs:           &sequenceIDs{},
		Users:         users,
		Codes:         codes,
		RefreshTokens: refreshRepo,
		Folders:       folderRepo,
		Account:       purger,
		Transactions:  store,
		Passwords:     plainHasher{},
		Digester:      digester,
		AccessTokens:  issuer,
		Denylist:      denylist,
		CodeLimiter:   memory.NewRateLimiter(fixed),
		LoginAttempts: memory.NewFailureGuard(fixed),
		Mailer:        mailer,
	})
	if err != nil {
		t.Fatalf("创建认证服务: %v", err)
	}

	/* 目录用例与认证共享存储，注册产生的根目录可直接被目录端点看到 */
	codec, err := folderusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	folderService, err := folderusecase.NewService(folderusecase.Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 1000},
		Folders:      folderRepo,
		Notes:        noteRepo,
		Transactions: store,
		PageTokens:   codec,
		Pages:        pageRepo,
		Jobs:         jobRepo,
		TagLinks:     noteTagRepo,
		Versions:     versionRepo,
		Shares:       shareRepo,
		Storage:      usage,
		Objects:      objects,
	})
	if err != nil {
		t.Fatalf("创建目录服务: %v", err)
	}

	/* 版本与分享共享存储：保存正文时生成的版本可被版本端点立即读到 */
	versionCodec, err := versionusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建版本分页编解码器: %v", err)
	}
	versionService, err := versionusecase.NewService(versionusecase.Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 6000},
		Versions:     versionRepo,
		Notes:        noteRepo,
		Transactions: store,
		PageTokens:   versionCodec,
	})
	if err != nil {
		t.Fatalf("创建版本服务: %v", err)
	}
	shareService, err := shareusecase.NewService(shareusecase.Deps{
		Clock:   fixed,
		Notes:   noteRepo,
		Folders: folderRepo,
		Pages:   pageRepo,
		Objects: objects,
		Shares:  shareRepo,
		BaseURL: "https://gleannote.example",
	})
	if err != nil {
		t.Fatalf("创建分享服务: %v", err)
	}

	noteCodec, err := noteusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建笔记分页编解码器: %v", err)
	}
	noteService, err := noteusecase.NewService(noteusecase.Deps{
		Clock:          fixed,
		IDs:            &sequenceIDs{next: 2000},
		Notes:          noteRepo,
		Folders:        folderRepo,
		FolderRestorer: testFolderRestorer{service: folderService},
		Uploads:        uploadRepo,
		Pages:          pageRepo,
		Jobs:           jobRepo,
		Quota:          ledger,
		Storage:        usage,
		Queue:          queue,
		Objects:        objects,
		TagLinks:       noteTagRepo,
		Versions:       versionService,
		Shares:         shareRepo,
		Transactions:   store,
		PageTokens:     noteCodec,
	})
	if err != nil {
		t.Fatalf("创建笔记服务: %v", err)
	}

	uploadCodec, err := uploadusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建页分页编解码器: %v", err)
	}
	uploadService, err := uploadusecase.NewService(uploadusecase.Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 3000},
		Uploads:      uploadRepo,
		Notes:        noteRepo,
		Pages:        pageRepo,
		Objects:      objects,
		Images:       imageprocessor.New(),
		Transactions: store,
		PageTokens:   uploadCodec,
	})
	if err != nil {
		t.Fatalf("创建上传服务: %v", err)
	}

	conversionService, err := conversionusecase.NewService(conversionusecase.Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 4000},
		Notes:        noteRepo,
		Pages:        pageRepo,
		Jobs:         jobRepo,
		Quota:        ledger,
		Storage:      usage,
		Queue:        queue,
		Vision:       visionProvider,
		Objects:      objects,
		Versions:     versionService,
		Transactions: store,
	})
	if err != nil {
		t.Fatalf("创建转换服务: %v", err)
	}

	tagCodec, err := tagusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建标签分页编解码器: %v", err)
	}
	tagService, err := tagusecase.NewService(tagusecase.Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 5000},
		Tags:         tagRepo,
		NoteTags:     noteTagRepo,
		Notes:        noteRepo,
		Transactions: store,
		PageTokens:   tagCodec,
	})
	if err != nil {
		t.Fatalf("创建标签服务: %v", err)
	}

	trashCodec, err := trashusecase.NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建回收站分页编解码器: %v", err)
	}
	changesService, err := changesusecase.NewService(changesusecase.Deps{
		Clock: fixed,
		Log:   changeLogRepo,
	})
	if err != nil {
		t.Fatalf("创建增量同步服务: %v", err)
	}

	trashService, err := trashusecase.NewService(trashusecase.Deps{
		Clock:      fixed,
		Folders:    folderRepo,
		Notes:      noteRepo,
		PageTokens: trashCodec,
	})
	if err != nil {
		t.Fatalf("创建回收站服务: %v", err)
	}

	/* 导出与导入任务共享存储；导出读取页面原图并写入对象存储 */
	exportService, err := exportusecase.NewService(exportusecase.Deps{
		Clock:   fixed,
		IDs:     &sequenceIDs{next: 7000},
		Notes:   noteRepo,
		Folders: folderRepo,
		Pages:   pageRepo,
		Objects: objects,
		Jobs:    exportJobRepo,
		PDF:     pdfdoc.New(),
	})
	if err != nil {
		t.Fatalf("创建导出服务: %v", err)
	}
	importService, err := importsusecase.NewService(importsusecase.Deps{
		Clock:       fixed,
		IDs:         &sequenceIDs{next: 8000},
		Folders:     folderRepo,
		Jobs:        importJobRepo,
		NoteCreator: noteService,
	})
	if err != nil {
		t.Fatalf("创建导入服务: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := httptransport.Deps{
		Env:                "dev",
		Logger:             logger,
		Clock:              fixed,
		Vision:             visionProvider,
		Mailer:             mail.NewFake(io.Discard, false),
		ObjectStore:        objects,
		Auth:               service,
		Folders:            folderService,
		Notes:              noteService,
		Uploads:            uploadService,
		Conversions:        conversionService,
		Tags:               tagService,
		Trash:              trashService,
		Versions:           versionService,
		Shares:             shareService,
		Changes:            changesService,
		Exports:            exportService,
		Imports:            importService,
		AccessTokens:       issuer,
		Denylist:           denylist,
		Idempotency:        memory.NewIdempotencyStore(fixed),
		PublicShareLimiter: memory.NewRateLimiter(fixed),
		Limiter:            memory.NewRateLimiter(fixed),
	}
	handler, err := httptransport.NewRouter(deps)
	if err != nil {
		t.Fatalf("装配路由: %v", err)
	}
	return &testApp{
		handler:  handler,
		deps:     deps,
		clock:    fixed,
		issuer:   issuer,
		users:    users,
		codes:    codes,
		folders:  folderRepo,
		notes:    noteRepo,
		tags:     tagRepo,
		uploads:  uploadRepo,
		pages:    pageRepo,
		jobs:     jobRepo,
		ledger:   ledger,
		objects:  objects,
		mail:     mailer,
		digester: digester,
	}
}

/* seedCode 直接写入一条已知验证码，绕开随机生成以获得可断言的输入 */
func (a *testApp) seedCode(t *testing.T, email, purpose, code string) {
	t.Helper()
	now := a.clock.Now()
	/* ID 带上邮箱，允许多个账号在同一夹具内各自持有验证码 */
	record := &entity.EmailCode{
		ID:        "code-" + purpose + "-" + email,
		Email:     email,
		Purpose:   purpose,
		CodeHash:  a.digester.Digest(email, purpose, code),
		ExpiresAt: now.Add(authusecase.VerificationCodeTTL),
		CreatedAt: now,
	}
	if err := a.codes.Create(context.Background(), record); err != nil {
		t.Fatalf("写入验证码: %v", err)
	}
}

/* httpResult 是测试侧的最小响应视图 */
type httpResult struct {
	status      int
	header      http.Header
	body        []byte
	contentType string
}

/* do 发送一次请求；body 非空时自动补 Content-Type，headers 覆盖额外头部 */
func (a *testApp) do(t *testing.T, method, path, body string, headers map[string]string) httpResult {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if body != "" && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}

	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, request)
	return httpResult{
		status:      recorder.Code,
		header:      recorder.Header(),
		body:        recorder.Body.Bytes(),
		contentType: recorder.Header().Get("Content-Type"),
	}
}

/* sessionBody 是注册与登录响应的解析结构，字段与契约一致 */
type sessionBody struct {
	User struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		Nickname      string `json:"nickname"`
		EmailVerified bool   `json:"emailVerified"`
		CreatedAt     string `json:"createdAt"`
		UpdatedAt     string `json:"updatedAt"`
	} `json:"user"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

/* decodeSession 解析会话响应，失败即终止测试 */
func decodeSession(t *testing.T, result httpResult) sessionBody {
	t.Helper()
	var session sessionBody
	if err := json.Unmarshal(result.body, &session); err != nil {
		t.Fatalf("解析会话响应失败: %v，原文 %q", err, string(result.body))
	}
	return session
}

/* problemBody 只取测试关切的错误字段 */
type problemBody struct {
	Type              string `json:"type"`
	Status            int    `json:"status"`
	Instance          string `json:"instance"`
	Code              string `json:"code"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
	RequestID         string `json:"requestId"`
}

/* decodeProblem 解析问题响应并校验 Content-Type 已切换到 problem+json */
func decodeProblem(t *testing.T, result httpResult) problemBody {
	t.Helper()
	if !strings.HasPrefix(result.contentType, "application/problem+json") {
		t.Fatalf("Content-Type = %q，期望 problem+json", result.contentType)
	}
	var body problemBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析问题响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* registerSession 走完整注册流程，验证码固定为 123456 */
func (a *testApp) registerSession(t *testing.T, email, password string) sessionBody {
	t.Helper()
	a.seedCode(t, email, entity.PurposeRegister, "123456")
	result := a.do(t, http.MethodPost, "/v1/auth:register",
		fmt.Sprintf(`{"email":%q,"code":"123456","password":%q,"agreements":{"userAgreement":true,"privacyPolicy":true}}`, email, password),
		map[string]string{"Idempotency-Key": "key-" + email})
	if result.status != http.StatusCreated {
		t.Fatalf("注册状态码 = %d，期望 201，原文 %q", result.status, string(result.body))
	}
	return decodeSession(t, result)
}

/* bearer 生成鉴权头部 */
func bearer(accessToken string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + accessToken}
}

/* jsonUnmarshal 是测试内的薄封装，避免各断言文件重复导入 encoding/json */
func jsonUnmarshal(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
