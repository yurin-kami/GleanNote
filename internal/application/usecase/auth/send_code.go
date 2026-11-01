/* 发送邮箱验证码：频率与日限额、场景合法性、旧码失效与事务外投递 */
package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/random"
)

/*
 * SendCode 生成 6 位验证码并投递到邮箱。
 * 顺序：邮箱与用途校验 → 频率限额 → 事务内失效旧码并落库 → 事务外投递。
 * 不区分账号是否已注册：注册码与重置码发送给任何邮箱都返回同一结果，避免账号枚举。
 * 投递失败按内部错误返回，已落库的记录保留至 10 分钟后自然过期。
 * 参考：docs/API.md#511-post-v1authsendcode、docs/ARCHITECTURE.md#87-限流与防滥用
 */
func (s *Service) SendCode(ctx context.Context, input SendCodeInput) error {
	email, err := valueobject.ParseEmail(input.Email)
	if err != nil {
		return err
	}
	purpose := strings.ToUpper(strings.TrimSpace(input.Purpose))
	if !entity.ValidCodePurpose(purpose) {
		return errs.New(errs.CodeValidationFailed, "purpose 必须为 REGISTER、RESET_PASSWORD 或 DELETE_ACCOUNT")
	}
	if err := s.takeCodeQuota(ctx, email.String()); err != nil {
		return err
	}

	/* 验证码只在内存中短暂存在；落库的是绑定邮箱与用途的 HMAC 摘要 */
	code, err := random.Digits(6)
	if err != nil {
		return errs.New(errs.CodeInternal, "生成验证码失败").WithCause(err)
	}
	codeID, err := s.newID("验证码")
	if err != nil {
		return err
	}
	now := s.clock.Now()
	record := &entity.EmailCode{
		ID:        codeID,
		Email:     email.String(),
		Purpose:   purpose,
		CodeHash:  s.deps.Digester.Digest(email.String(), purpose, code),
		ExpiresAt: now.Add(VerificationCodeTTL),
		CreatedAt: now,
	}

	/* 失效旧码与新码写入必须原子，避免同一用途出现两条可用验证码 */
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.Codes.InvalidateActive(txCtx, email.String(), purpose, now); err != nil {
			return err
		}
		return s.deps.Codes.Create(txCtx, record)
	})
	if err != nil {
		return err
	}
	return s.deliverCode(ctx, email.String(), purpose, code)
}

/* takeCodeQuota 依次占用「60 秒一次」与「每日 10 次」两个窗口。
 * 键使用邮箱摘要而非明文，符合限流键脱敏要求（docs/ARCHITECTURE.md#87-限流与防滥用）。
 */
func (s *Service) takeCodeQuota(ctx context.Context, email string) error {
	identity := s.deps.Digester.Digest(email)
	allowed, wait, err := s.deps.CodeLimiter.Take(ctx, "rl:code:min:"+identity, 1, CodeSendWindow)
	if err != nil {
		return errs.New(errs.CodeInternal, "验证码频率校验失败").WithCause(err)
	}
	if !allowed {
		return withRetry(errs.New(errs.CodeAuthCodeRateLimited, "验证码发送过于频繁"), wait)
	}

	allowed, wait, err = s.deps.CodeLimiter.Take(ctx, "rl:code:day:"+identity, CodeDailyLimit, CodeDailyWindow)
	if err != nil {
		return errs.New(errs.CodeInternal, "验证码日限额校验失败").WithCause(err)
	}
	if !allowed {
		return withRetry(errs.New(errs.CodeAuthCodeRateLimited, "验证码每日发送次数已达上限"), wait)
	}
	return nil
}

/* deliverCode 在事务外投递邮件；端口错误统一归为内部错误并保留原因供日志 */
func (s *Service) deliverCode(ctx context.Context, email, purpose, code string) error {
	subject, body := codeMail(purpose, code)
	message := port.MailMessage{To: email, Subject: subject, Body: body}
	if err := s.deps.Mailer.Send(ctx, message); err != nil {
		return errs.New(errs.CodeInternal, "验证码邮件投递失败").WithCause(err)
	}
	return nil
}

/* codeMail 按用途生成邮件主题与正文，正文含有效期分钟数 */
func codeMail(purpose, code string) (string, string) {
	minutes := int(VerificationCodeTTL / time.Minute)
	subject := "拾页笔记 · 注册验证码"
	switch purpose {
	case entity.PurposeResetPassword:
		subject = "拾页笔记 · 重置密码验证码"
	case entity.PurposeDeleteAccount:
		subject = "拾页笔记 · 注销账号验证码"
	}
	body := fmt.Sprintf("你的验证码是 %s，%d 分钟内有效。\n如非本人操作，请忽略本邮件。\n", code, minutes)
	return subject, body
}
