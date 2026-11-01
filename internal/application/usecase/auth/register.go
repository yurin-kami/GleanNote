/* 注册用例：验证码校验、账号与根目录创建、刷新令牌落库与事务后签发 */
package auth

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * Register 完成注册并返回登录态。
 * 顺序：入参校验 → 验证码只读预校验 → 邮箱占用预检 → 密码哈希 → 单事务（验证码、账号、根目录、刷新令牌）→ 签发访问令牌。
 * Argon2id 必须在验证码通过后执行，避免未认证请求反复触发内存困难计算。
 * 邮件投递不在本用例内；验证码由 SendCode 提前发送。
 * 参考：docs/API.md#512-post-v1authregister、docs/ARCHITECTURE.md#61-注册与登录
 */
func (s *Service) Register(ctx context.Context, input RegisterInput) (Session, error) {
	email, err := valueobject.ParseEmail(input.Email)
	if err != nil {
		return Session{}, err
	}
	if !input.Agreements.UserAgreement || !input.Agreements.PrivacyPolicy {
		return Session{}, errs.New(errs.CodeAuthAgreementRequired, "必须同时同意用户协议与隐私政策")
	}
	if err := valueobject.CheckPasswordPolicy(input.Password); err != nil {
		return Session{}, err
	}
	nickname, err := registerNickname(email, input.Nickname)
	if err != nil {
		return Session{}, err
	}
	deviceID, err := normalizeDeviceID(input.DeviceID)
	if err != nil {
		return Session{}, err
	}
	if err := validateVerificationCode(input.Code); err != nil {
		return Session{}, err
	}

	now := s.clock.Now()
	/* 只读预校验：非法验证码直接拒绝并记录失败次数，不进入 Argon2id 计算 */
	if err := s.checkCode(ctx, email.String(), entity.PurposeRegister, input.Code, now); err != nil {
		return Session{}, s.recordCodeFailure(ctx, email.String(), entity.PurposeRegister, input.Code, now)
	}

	/* 邮箱已占用时直接拒绝，避免白白消耗一条验证码 */
	if _, err := s.deps.Users.GetByEmail(ctx, email.String()); err == nil {
		return Session{}, errs.New(errs.CodeAuthEmailAlreadyExists, "邮箱已注册")
	} else if !isNotFound(err) {
		return Session{}, err
	}
	passwordHash, err := s.deps.Passwords.Hash(ctx, input.Password)
	if err != nil {
		return Session{}, errs.New(errs.CodeInternal, "生成密码哈希失败").WithCause(err)
	}

	/* 账号初始化是原子操作：验证码消费、账号、根目录与刷新令牌同生共死 */
	var user *entity.User
	var refreshRaw string
	err = s.withinCodeTx(ctx, func(txCtx context.Context) error {
		if _, err := s.verifyCode(txCtx, email.String(), entity.PurposeRegister, input.Code, now); err != nil {
			return err
		}
		userID, err := s.newID("用户")
		if err != nil {
			return err
		}
		created, err := entity.NewUser(userID, email, passwordHash, nickname, now)
		if err != nil {
			return err
		}
		if err := s.deps.Users.Create(txCtx, created); err != nil {
			return err
		}
		rootID, err := s.newID("根目录")
		if err != nil {
			return err
		}
		if err := s.deps.Folders.Create(txCtx, entity.NewRootFolder(rootID, created.ID, now)); err != nil {
			return err
		}
		raw, token, err := s.prepareRefresh(created.ID, deviceID, now)
		if err != nil {
			return err
		}
		if err := s.deps.RefreshTokens.Create(txCtx, token); err != nil {
			return err
		}
		user, refreshRaw = created, raw
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return s.newSession(user, refreshRaw, deviceID, now)
}

/* registerNickname 昵称为空时取邮箱前缀，非空时交给值对象归一化与校验 */
func registerNickname(email valueobject.Email, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return valueobject.DefaultNickname(email), nil
	}
	return valueobject.NormalizeNickname(raw)
}
