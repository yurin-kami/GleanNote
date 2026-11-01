/* 账号资料用例：当前用户、昵称修改与注销 */
package auth

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* GetUser 返回当前账号的对外视图；不存在按 404 处理，便于统一防枚举 */
func (s *Service) GetUser(ctx context.Context, userID string) (UserView, error) {
	if userID == "" {
		return UserView{}, errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	user, err := s.deps.Users.GetByID(ctx, userID)
	if err != nil {
		return UserView{}, err
	}
	return toUserView(user), nil
}

/*
 * UpdateNickname 修改昵称并返回最新视图。
 * 昵称的归一到长度校验都由实体负责，用例只处理事务与读写顺序。
 * 参考：docs/API.md#517-用户与额度
 */
func (s *Service) UpdateNickname(ctx context.Context, userID, nickname string) (UserView, error) {
	if userID == "" {
		return UserView{}, errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	now := s.clock.Now()
	var view UserView
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		user, err := s.deps.Users.GetByID(txCtx, userID)
		if err != nil {
			return err
		}
		if err := user.Rename(nickname, now); err != nil {
			return err
		}
		if err := s.deps.Users.Update(txCtx, user); err != nil {
			return err
		}
		view = toUserView(user)
		return nil
	})
	if err != nil {
		return UserView{}, err
	}
	return view, nil
}

/*
 * DeleteAccount 注销账号：验证码二次确认后物理删除。
 * 顺序：确认标记 → 验证码 → 单事务（清理全部业务数据含刷新令牌、删除账号）。
 * 对象存储中的图片由后台补偿任务异步清理，不在本事务内处理（G-07）。
 * 参考：docs/API.md#517-用户与额度、docs/ARCHITECTURE.md#812-安全与隐私
 */
func (s *Service) DeleteAccount(ctx context.Context, input DeleteAccountInput) error {
	if input.UserID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	if !input.Confirm {
		return errs.New(errs.CodeValidationFailed, "必须显式确认注销账号")
	}
	if err := validateVerificationCode(input.Code); err != nil {
		return err
	}

	user, err := s.deps.Users.GetByID(ctx, input.UserID)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	return s.withinCodeTx(ctx, func(txCtx context.Context) error {
		if _, err := s.verifyCode(txCtx, user.Email, entity.PurposeDeleteAccount, input.Code, now); err != nil {
			return err
		}
		if err := s.deps.Account.PurgeForUser(txCtx, user.ID); err != nil {
			return err
		}
		return s.deps.Users.Delete(txCtx, user.ID)
	})
}
