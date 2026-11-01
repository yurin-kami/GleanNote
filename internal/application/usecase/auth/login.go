/* 登录用例：锁定检查、口令校验、失败计数与刷新令牌落库 */
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * Login 校验邮箱口令并返回新的登录态。
 * 顺序：锁定状态 → 账号存在性 → 口令校验（失败计数）→ 事务内写刷新令牌 → 签发访问令牌。
 * 账号不存在与口令错误返回同一错误码，并对占位哈希做一次等时校验，避免账号枚举与响应时延差异。
 * 锁定期内直接返回 429，不查库也不做口令运算，避免被用作 CPU 放大器。
 * 参考：docs/API.md#513-post-v1authlogin、docs/ARCHITECTURE.md#86-认证与授权
 */
func (s *Service) Login(ctx context.Context, input LoginInput) (Session, error) {
	email, err := valueobject.ParseEmail(input.Email)
	if err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(input.Password) == "" {
		return Session{}, errs.New(errs.CodeValidationFailed, "密码不能为空")
	}
	deviceID, err := normalizeDeviceID(input.DeviceID)
	if err != nil {
		return Session{}, err
	}
	now := s.clock.Now()

	lockKey := s.loginLockKey(email.String(), input.ClientIP)
	locked, wait, err := s.deps.LoginAttempts.Locked(ctx, lockKey)
	if err != nil {
		return Session{}, errs.New(errs.CodeInternal, "查询登录锁定状态失败").WithCause(err)
	}
	if locked {
		return Session{}, withRetry(errs.New(errs.CodeAuthTooManyAttempts, "密码连续错误次数过多"), wait)
	}

	user, err := s.deps.Users.GetByEmail(ctx, email.String())
	if err != nil {
		if !isNotFound(err) {
			return Session{}, err
		}
		/* 未注册邮箱按口令错误处理：占位哈希校验保证与真实账号的响应时延一致 */
		_, _ = s.deps.Passwords.Verify(ctx, s.dummyHash, input.Password)
		return Session{}, s.recordLoginFailure(ctx, lockKey)
	}
	matched, err := s.deps.Passwords.Verify(ctx, user.PasswordHash, input.Password)
	if err != nil {
		return Session{}, errs.New(errs.CodeInternal, "校验密码失败").WithCause(err)
	}
	if !matched {
		return Session{}, s.recordLoginFailure(ctx, lockKey)
	}
	if err := s.deps.LoginAttempts.Clear(ctx, lockKey); err != nil {
		return Session{}, errs.New(errs.CodeInternal, "清空登录失败计数失败").WithCause(err)
	}
	return s.startSession(ctx, user, deviceID, now)
}

/*
 * loginLockKey 生成失败计数键：邮箱摘要与客户端 IP 同时参与。
 * 只按邮箱计数会让攻击者用 5 次错误口令锁死他人账号，因此必须绑定来源。
 */
func (s *Service) loginLockKey(email, clientIP string) string {
	return "login:" + s.deps.Digester.Digest(email) + ":" + s.deps.Digester.Digest(clientIP)
}

/* recordLoginFailure 累加失败次数；达到阈值时返回 429 与剩余锁定秒数 */
func (s *Service) recordLoginFailure(ctx context.Context, lockKey string) error {
	locked, wait, err := s.deps.LoginAttempts.Fail(ctx, lockKey, LoginFailureLimit, LoginLockDuration)
	if err != nil {
		return errs.New(errs.CodeInternal, "记录登录失败次数失败").WithCause(err)
	}
	if locked {
		return withRetry(errs.New(errs.CodeAuthTooManyAttempts, "密码连续错误次数过多"), wait)
	}
	return errs.New(errs.CodeAuthInvalidCredentials, "邮箱或密码不正确")
}

/*
 * startSession 在单事务内写入刷新令牌，提交后再签发访问令牌。
 * 拆分原因：JWT 签名与随机数生成属于外部计算，不放进数据库事务（G-07）。
 */
func (s *Service) startSession(ctx context.Context, user *entity.User, deviceID string, now time.Time) (Session, error) {
	var refreshRaw string
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		raw, token, err := s.prepareRefresh(user.ID, deviceID, now)
		if err != nil {
			return err
		}
		if err := s.deps.RefreshTokens.Create(txCtx, token); err != nil {
			return err
		}
		refreshRaw = raw
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return s.newSession(user, refreshRaw, deviceID, now)
}
