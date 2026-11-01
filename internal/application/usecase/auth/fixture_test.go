/* 认证用例测试夹具：真实内存仓储 + 可预测的哈希/ID/邮件替身 */
package auth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/codehash"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/token"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* stubHasher 用可预测编码替代 Argon2id，使用例测试聚焦流程而非哈希成本 */
type stubHasher struct {
	hashCalls int
}

/* Hash 返回带前缀的明文，便于断言最终落库的凭证 */
func (h *stubHasher) Hash(ctx context.Context, plain string) (string, error) {
	h.hashCalls++
	return "stub:" + plain, nil
}

/* Verify 只认本替身生成的编码，语义与真实实现一致 */
func (h *stubHasher) Verify(ctx context.Context, encoded, plain string) (bool, error) {
	return encoded == "stub:"+plain, nil
}

/* recordingMailer 收集投递内容，供验证码与主题断言 */
type recordingMailer struct {
	sent []port.MailMessage
}

/* Name 返回替身名称 */
func (*recordingMailer) Name() string { return "recording" }

/* Send 记录邮件并立即成功 */
func (m *recordingMailer) Send(ctx context.Context, msg port.MailMessage) error {
	m.sent = append(m.sent, msg)
	return nil
}

/* Health 恒为健康 */
func (*recordingMailer) Health(ctx context.Context) error { return nil }

/* folderRecorder 记录注册时的根目录写入，避免测试依赖目录模块的查询能力 */
type folderRecorder struct {
	created []*entity.Folder
}

/* Create 记录根目录写入并在 ID 重复时报错，语义与内存实现一致 */
func (r *folderRecorder) Create(ctx context.Context, folder *entity.Folder) error {
	for _, existing := range r.created {
		if existing.ID == folder.ID {
			return errs.New(errs.CodeInternal, "目录 ID 冲突")
		}
	}
	r.created = append(r.created, folder)
	return nil
}

/* accountRecorder 记录注销时的账号数据清理，避免测试依赖各业务仓储实现 */
type accountRecorder struct {
	purged []string
}

/* PurgeForUser 记录账号级清理调用 */
func (r *accountRecorder) PurgeForUser(ctx context.Context, userID string) error {
	r.purged = append(r.purged, userID)
	return nil
}

/* sequenceIDs 生成可读的自增 ID，便于断言与排查 */
type sequenceIDs struct {
	next int
}

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* fixture 汇总一次测试所需的全部依赖 */
type fixture struct {
	service  *Service
	clock    *clock.Fixed
	mail     *recordingMailer
	hasher   *stubHasher
	issuer   *token.Ed25519
	users    *memory.UserRepository
	codes    *memory.EmailCodeRepository
	tokens   *memory.RefreshTokenRepository
	folders  *folderRecorder
	account  *accountRecorder
	limiter  *memory.RateLimiter
	guard    *memory.FailureGuard
	denylist *memory.AccessTokenDenylist
	digester *codehash.Hasher
}

/* newFixture 装配一套共享同一内存存储的认证依赖；时间固定以便断言有效期 */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()

	digester, err := codehash.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("创建验证码摘要器: %v", err)
	}
	issuer, err := token.NewEd25519(nil)
	if err != nil {
		t.Fatalf("创建令牌签发器: %v", err)
	}

	f := &fixture{
		clock:    fixed,
		mail:     &recordingMailer{},
		hasher:   &stubHasher{},
		issuer:   issuer,
		users:    memory.NewUserRepository(store),
		codes:    memory.NewEmailCodeRepository(store),
		tokens:   memory.NewRefreshTokenRepository(store),
		folders:  &folderRecorder{},
		account:  &accountRecorder{},
		limiter:  memory.NewRateLimiter(fixed),
		guard:    memory.NewFailureGuard(fixed),
		denylist: memory.NewAccessTokenDenylist(fixed),
		digester: digester,
	}

	service, err := NewService(Deps{
		Clock:         fixed,
		IDs:           &sequenceIDs{},
		Users:         f.users,
		Codes:         f.codes,
		RefreshTokens: f.tokens,
		Folders:       f.folders,
		Account:       f.account,
		Transactions:  store,
		Passwords:     f.hasher,
		Digester:      digester,
		AccessTokens:  issuer,
		Denylist:      f.denylist,
		CodeLimiter:   f.limiter,
		LoginAttempts: f.guard,
		Mailer:        f.mail,
	})
	if err != nil {
		t.Fatalf("创建认证服务: %v", err)
	}
	f.service = service
	return f
}

/* seedCode 直接写入一条已知验证码，绕开随机生成以获得可断言的输入 */
func (f *fixture) seedCode(t *testing.T, email, purpose, code string) *entity.EmailCode {
	t.Helper()

	now := f.clock.Now()
	record := &entity.EmailCode{
		ID:        "code-" + purpose,
		Email:     email,
		Purpose:   purpose,
		CodeHash:  f.digester.Digest(email, purpose, code),
		ExpiresAt: now.Add(VerificationCodeTTL),
		CreatedAt: now,
	}
	if err := f.codes.Create(context.Background(), record); err != nil {
		t.Fatalf("写入验证码: %v", err)
	}
	return record
}

/* seedUser 直接写入账号，供登录与验证码场景的账号存在性测试 */
func (f *fixture) seedUser(t *testing.T, email, password string) *entity.User {
	t.Helper()

	parsed, err := valueobject.ParseEmail(email)
	if err != nil {
		t.Fatalf("解析邮箱: %v", err)
	}
	hash, err := f.hasher.Hash(context.Background(), password)
	if err != nil {
		t.Fatalf("生成密码哈希: %v", err)
	}
	nickname, err := valueobject.NormalizeNickname("测试用户")
	if err != nil {
		t.Fatalf("归一化昵称: %v", err)
	}
	user, err := entity.NewUser("seeded-user", parsed, hash, nickname, f.clock.Now())
	if err != nil {
		t.Fatalf("创建实体: %v", err)
	}
	if err := f.users.Create(context.Background(), user); err != nil {
		t.Fatalf("写入账号: %v", err)
	}
	return user
}

/* register 走完整注册流程，验证码固定为 123456 */
func (f *fixture) register(t *testing.T, email, password string) Session {
	t.Helper()

	f.seedCode(t, email, entity.PurposeRegister, "123456")
	session, err := f.service.Register(context.Background(), RegisterInput{
		Email:      email,
		Code:       "123456",
		Password:   password,
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	return session
}

/* sessionErr 把「会话 + 错误」的调用折叠为单个错误，便于直接断言错误码 */
func sessionErr(_ Session, err error) error {
	return err
}

/* requireCode 断言错误链中包含期望的业务错误码 */
func requireCode(t *testing.T, err error, want errs.Code) *errs.Error {
	t.Helper()

	if err == nil {
		t.Fatalf("期望错误 %s，实际成功", want)
	}
	business, ok := errs.As(err)
	if !ok || business.Code() != want {
		t.Fatalf("期望错误码 %s，实际 %v", want, err)
	}
	return business
}
