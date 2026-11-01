/* 刷新用例：令牌轮换、重放检测与按设备吊销 */
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * Refresh 用刷新令牌换取新的令牌对。
 * 规则：每次使用即轮换；已轮换令牌再次出现视为重放，吊销该设备全部令牌。
 * 并发：令牌行在事务内以 SELECT ... FOR UPDATE 锁定，轮换写为条件更新，
 * 两个并发请求只有一个能成功，另一个按重放处置。
 * 参考：docs/API.md#514-post-v1authrefresh、docs/ARCHITECTURE.md#86-认证与授权
 */
func (s *Service) Refresh(ctx context.Context, input RefreshInput) (Session, error) {
	raw := strings.TrimSpace(input.RefreshToken)
	if raw == "" {
		return Session{}, errs.New(errs.CodeAuthTokenInvalid, "刷新令牌不能为空")
	}
	deviceID, err := normalizeDeviceID(input.DeviceID)
	if err != nil {
		return Session{}, err
	}
	now := s.clock.Now()
	hash := hashRefreshToken(raw)

	/* 先探测一次状态：重放处置要独立提交，不能随业务错误回滚 */
	probe, err := s.deps.RefreshTokens.GetByHash(ctx, hash)
	if err != nil {
		if isNotFound(err) {
			return Session{}, errs.New(errs.CodeAuthTokenInvalid, "刷新令牌不存在")
		}
		return Session{}, err
	}
	if probe.Rotated() {
		return Session{}, s.rejectReplay(ctx, probe, now)
	}
	if !probe.Usable(now) {
		return Session{}, errs.New(errs.CodeAuthTokenInvalid, "刷新令牌已失效")
	}
	if deviceID != "" && probe.DeviceID != "" && deviceID != probe.DeviceID {
		return Session{}, errs.New(errs.CodeAuthTokenInvalid, "设备标识与刷新令牌不匹配")
	}

	user, err := s.deps.Users.GetByID(ctx, probe.UserID)
	if err != nil {
		if isNotFound(err) {
			return Session{}, errs.New(errs.CodeAuthTokenInvalid, "账号不存在")
		}
		return Session{}, err
	}

	/* 旧令牌吊销与新令牌写入必须原子，避免出现两个可用刷新令牌 */
	var refreshRaw string
	var rotated *entity.RefreshToken
	replayed := false
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		stored, err := s.deps.RefreshTokens.GetForUpdate(txCtx, hash)
		if err != nil {
			if isNotFound(err) {
				return errs.New(errs.CodeAuthTokenInvalid, "刷新令牌不存在")
			}
			return err
		}
		/* 锁内再判一次：并发请求可能已在本事务开始前完成轮换 */
		if stored.Rotated() {
			replayed = true
			return s.deps.RefreshTokens.RevokeDevice(txCtx, stored.UserID, stored.DeviceID, now)
		}
		if !stored.Usable(now) {
			return errs.New(errs.CodeAuthTokenInvalid, "刷新令牌已失效")
		}
		if deviceID != "" && stored.DeviceID != "" && deviceID != stored.DeviceID {
			return errs.New(errs.CodeAuthTokenInvalid, "设备标识与刷新令牌不匹配")
		}
		newRaw, next, err := s.prepareRefresh(user.ID, stored.DeviceID, now)
		if err != nil {
			return err
		}
		stored.Revoke(now, next.ID)
		applied, err := s.deps.RefreshTokens.Update(txCtx, stored)
		if err != nil {
			return err
		}
		if !applied {
			/* 条件更新未命中说明已被并发轮换，整单按重放处置 */
			replayed = true
			return s.deps.RefreshTokens.RevokeDevice(txCtx, stored.UserID, stored.DeviceID, now)
		}
		if err := s.deps.RefreshTokens.Create(txCtx, next); err != nil {
			return err
		}
		refreshRaw, rotated = newRaw, next
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if replayed {
		return Session{}, errs.New(errs.CodeAuthRefreshReused, "刷新令牌已被使用，已吊销该设备登录态")
	}
	return s.newSession(user, refreshRaw, rotated.DeviceID, now)
}

/* rejectReplay 吊销令牌所属设备的全部令牌，再返回重放错误；吊销写入独立提交 */
func (s *Service) rejectReplay(ctx context.Context, stored *entity.RefreshToken, now time.Time) error {
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		return s.deps.RefreshTokens.RevokeDevice(txCtx, stored.UserID, stored.DeviceID, now)
	})
	if err != nil {
		return err
	}
	return errs.New(errs.CodeAuthRefreshReused, "刷新令牌已被使用，已吊销该设备登录态")
}
