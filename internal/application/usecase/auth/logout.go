/* 退出用例：访问令牌加入黑名单，刷新令牌按设备或全量吊销 */
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * Logout 结束当前登录态。
 * 顺序：访问令牌 jti 写入黑名单（TTL 至过期）→ 事务内吊销刷新令牌。
 * 幂等：令牌不存在或不属于当前用户时静默成功，避免枚举他人令牌。
 * 参考：docs/API.md#515-post-v1authlogout、docs/ARCHITECTURE.md#86-认证与授权
 */
func (s *Service) Logout(ctx context.Context, input LogoutInput) error {
	if input.UserID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	if input.TokenID != "" {
		if err := s.deps.Denylist.Add(ctx, input.TokenID, input.AccessExpiresAt); err != nil {
			return errs.New(errs.CodeInternal, "写入令牌黑名单失败").WithCause(err)
		}
	}
	now := s.clock.Now()
	return s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if input.AllDevices {
			return s.deps.RefreshTokens.RevokeAll(txCtx, input.UserID, now)
		}
		return s.revokePresentedRefresh(txCtx, input.UserID, input.RefreshToken, now)
	})
}

/* revokePresentedRefresh 只吊销本次携带的刷新令牌；空值与未知令牌视为已退出 */
func (s *Service) revokePresentedRefresh(ctx context.Context, userID, raw string, now time.Time) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	stored, err := s.deps.RefreshTokens.GetByHash(ctx, hashRefreshToken(trimmed))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if stored.UserID != userID {
		return nil
	}
	/* 已吊销（含已轮换）的令牌无需再写，保持原有替换指向以保留重放证据 */
	if stored.RevokedAt != nil {
		return nil
	}
	stored.Revoke(now, "")
	_, err = s.deps.RefreshTokens.Update(ctx, stored)
	return err
}
