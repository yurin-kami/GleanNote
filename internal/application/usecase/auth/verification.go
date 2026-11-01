/* 验证码校验的共享实现：错误次数落库与「失败也提交事务」的语义集中在此 */
package auth

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * codeFailure 表示验证码校验失败，且错误次数已在事务内写入。
 * 该错误必须让事务提交，否则「第 5 次失败即作废验证码」的规则会随回滚丢失。
 */
type codeFailure struct {
	business *errs.Error
}

/* Error 让 codeFailure 满足 error 接口，便于在事务闭包内直接返回 */
func (f *codeFailure) Error() string {
	return f.business.Error()
}

/* Unwrap 暴露业务错误，使调用方仍可用 errors.As 取到 *errs.Error */
func (f *codeFailure) Unwrap() error {
	return f.business
}

/*
 * withinCodeTx 在单个事务内执行 fn。
 * 语义：fn 返回 *codeFailure 时提交当前写入并把业务错误上抛；其他错误回滚；成功提交。
 * 约束：只包裹数据库操作，投递邮件等外部调用必须在事务外完成（G-07）。
 */
func (s *Service) withinCodeTx(ctx context.Context, fn func(ctx context.Context) error) error {
	var rejected *errs.Error
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		inner := fn(txCtx)
		if inner == nil {
			return nil
		}
		failure, ok := inner.(*codeFailure)
		if !ok {
			return inner
		}
		rejected = failure.business
		return nil
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	return nil
}

/*
 * checkCode 只读校验验证码，不消费也不累计错误次数。
 * 用途：在执行 Argon2id 等昂贵计算前快速拒绝非法输入，避免未认证请求放大 CPU 消耗。
 */
func (s *Service) checkCode(ctx context.Context, email, purpose, code string, now time.Time) error {
	record, err := s.deps.Codes.LatestActive(ctx, email, purpose)
	if err != nil {
		if isNotFound(err) {
			return errs.New(errs.CodeAuthCodeInvalid, "验证码不存在，请重新获取")
		}
		return err
	}
	return record.Check(s.deps.Digester.Digest(email, purpose, code), now)
}

/*
 * recordCodeFailure 在事务内正式校验并记录一次失败。
 * 用途：checkCode 快速失败后仍要落库错误次数，否则「第 5 次失败即作废」不会生效。
 */
func (s *Service) recordCodeFailure(ctx context.Context, email, purpose, code string, now time.Time) error {
	return s.withinCodeTx(ctx, func(txCtx context.Context) error {
		if _, err := s.verifyCode(txCtx, email, purpose, code, now); err != nil {
			return err
		}
		/* 并发下验证码可能已被替换并通过校验：按失败返回，避免调用方误以为校验成功 */
		return &codeFailure{business: errs.New(errs.CodeAuthCodeInvalid, "验证码校验状态已变化，请重新获取")}
	})
}

/*
 * verifyCode 校验并消费一条验证码。
 * 通过时返回已标记消费的记录（已持久化）；失败时把错误次数写回仓储并返回 *codeFailure。
 * 约束：必须在 withinCodeTx 内调用，否则错误次数的写入会被事务回滚。
 */
func (s *Service) verifyCode(ctx context.Context, email, purpose, code string, now time.Time) (*entity.EmailCode, error) {
	record, err := s.deps.Codes.LatestActive(ctx, email, purpose)
	if err != nil {
		if isNotFound(err) {
			return nil, &codeFailure{business: errs.New(errs.CodeAuthCodeInvalid, "验证码不存在，请重新获取")}
		}
		return nil, err
	}

	/* 摘要绑定邮箱、用途与验证码，跨场景复用同一验证码无法通过校验 */
	digest := s.deps.Digester.Digest(email, purpose, code)
	if err := record.Consume(digest, now); err != nil {
		business, ok := errs.As(err)
		if !ok {
			return nil, err
		}
		if updateErr := s.deps.Codes.Update(ctx, record); updateErr != nil {
			return nil, updateErr
		}
		return nil, &codeFailure{business: business}
	}
	if err := s.deps.Codes.Update(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}
