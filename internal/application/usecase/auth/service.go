/* 认证用例的依赖装配与共享常量；构造函数拒绝缺失依赖，避免运行期空指针 */
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
)

/*
 * 认证流程的时间与阈值常量。
 * 口径：验证码 10 分钟、同邮箱 60 秒一次、每日 10 次、刷新令牌 30 天、
 * 连续 5 次密码错误锁定 15 分钟（docs/ARCHITECTURE.md#86-认证与授权）。
 */
const (
	VerificationCodeTTL = 10 * time.Minute
	CodeSendWindow      = 60 * time.Second
	CodeDailyLimit      = 10
	CodeDailyWindow     = 24 * time.Hour
	RefreshTokenTTL     = 30 * 24 * time.Hour
	LoginFailureLimit   = 5
	LoginLockDuration   = 15 * time.Minute
	maxDeviceIDLength   = 128
)

/* Deps 是认证用例所需的端口集合，全部由 bootstrap 注入 */
type Deps struct {
	Clock         clock.Clock
	IDs           idgen.Generator
	Users         port.UserRepository
	Codes         port.EmailCodeRepository
	RefreshTokens port.RefreshTokenRepository
	Folders       port.FolderWriter
	/* Account 负责注销时的全量业务数据清理，避免认证包逐个依赖各业务仓储 */
	Account       port.AccountDataPurger
	Transactions  port.Transactor
	Passwords     port.PasswordHasher
	Digester      port.CodeHasher
	AccessTokens  port.AccessTokenIssuer
	Denylist      port.AccessTokenDenylist
	CodeLimiter   port.RateLimiter
	LoginAttempts port.FailureGuard
	Mailer        port.Mailer
}

/* placeholderPassword 只用于生成占位哈希，不参与任何真实账号的校验 */
const placeholderPassword = "gleannote-placeholder-password"

/* Service 是无状态用例编排器；所有可变状态都在仓储或端口中 */
type Service struct {
	deps  Deps
	clock clock.Clock
	ids   idgen.Generator
	/* dummyHash 供未注册邮箱登录时做一次等时口令校验，避免通过响应时延枚举账号 */
	dummyHash string
}

/* NewService 校验必需依赖并创建服务；缺失依赖属于装配错误，必须启动失败 */
func NewService(deps Deps) (*Service, error) {
	required := []struct {
		name string
		set  bool
	}{
		{"Clock", deps.Clock != nil},
		{"IDs", deps.IDs != nil},
		{"Users", deps.Users != nil},
		{"Codes", deps.Codes != nil},
		{"RefreshTokens", deps.RefreshTokens != nil},
		{"Folders", deps.Folders != nil},
		{"Account", deps.Account != nil},
		{"Transactions", deps.Transactions != nil},
		{"Passwords", deps.Passwords != nil},
		{"Digester", deps.Digester != nil},
		{"AccessTokens", deps.AccessTokens != nil},
		{"Denylist", deps.Denylist != nil},
		{"CodeLimiter", deps.CodeLimiter != nil},
		{"LoginAttempts", deps.LoginAttempts != nil},
		{"Mailer", deps.Mailer != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("认证用例缺少依赖: %v", missing)
	}

	/* 占位哈希在启动时计算一次：未注册邮箱的登录也能执行一次等时口令校验 */
	dummyHash, err := deps.Passwords.Hash(context.Background(), placeholderPassword)
	if err != nil {
		return nil, fmt.Errorf("认证用例生成占位口令哈希: %w", err)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs, dummyHash: dummyHash}, nil
}
