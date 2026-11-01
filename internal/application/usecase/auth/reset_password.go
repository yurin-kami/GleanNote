/* 重置密码用例：验证码校验、口令更换与全部刷新令牌吊销 */
package auth

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * ResetPassword 通过邮箱验证码重置密码。
 * 顺序：入参校验 → 账号存在性 → 验证码只读预校验 → 密码哈希 → 单事务（验证码、密码、刷新令牌全量吊销）。
 * 未注册邮箱与错误验证码返回同一错误码，避免账号枚举；Argon2id 只在验证码通过后执行。
 * 成功后全部设备需要重新登录，符合 PRD FR-01。
 * 参考：docs/API.md#516-post-v1authresetpassword、docs/ARCHITECTURE.md#86-认证与授权
 */
func (s *Service) ResetPassword(ctx context.Context, input ResetPasswordInput) error {
	email, err := valueobject.ParseEmail(input.Email)
	if err != nil {
		return err
	}
	if err := valueobject.CheckPasswordPolicy(input.NewPassword); err != nil {
		return err
	}
	if err := validateVerificationCode(input.Code); err != nil {
		return err
	}

	user, err := s.deps.Users.GetByEmail(ctx, email.String())
	if err != nil {
		if isNotFound(err) {
			return errs.New(errs.CodeAuthCodeInvalid, "验证码不存在，请重新获取")
		}
		return err
	}

	now := s.clock.Now()
	/* 只读预校验：非法验证码直接拒绝并记录失败次数，不进入 Argon2id 计算 */
	if err := s.checkCode(ctx, email.String(), entity.PurposeResetPassword, input.Code, now); err != nil {
		return s.recordCodeFailure(ctx, email.String(), entity.PurposeResetPassword, input.Code, now)
	}

	/* Argon2id 属于 CPU 密集计算，放在事务外，避免长时间持有数据库事务 */
	passwordHash, err := s.deps.Passwords.Hash(ctx, input.NewPassword)
	if err != nil {
		return errs.New(errs.CodeInternal, "生成密码哈希失败").WithCause(err)
	}

	err = s.withinCodeTx(ctx, func(txCtx context.Context) error {
		if _, err := s.verifyCode(txCtx, email.String(), entity.PurposeResetPassword, input.Code, now); err != nil {
			return err
		}
		if err := user.ChangePassword(passwordHash, now); err != nil {
			return err
		}
		if err := s.deps.Users.Update(txCtx, user); err != nil {
			return err
		}
		return s.deps.RefreshTokens.RevokeAll(txCtx, user.ID, now)
	})
	if err != nil {
		return err
	}

	/* 密码已更换，旧的失败计数不应继续阻挡登录；清理失败不影响本次成功 */
	_ = s.deps.LoginAttempts.Clear(ctx, s.loginLockKey(email.String(), input.ClientIP))
	return nil
}
