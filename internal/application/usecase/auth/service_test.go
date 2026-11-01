/* 认证用例的单元测试：覆盖发送验证码、注册、登录、刷新、登出、重置与注销 */
package auth

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* sixDigits 从邮件正文提取 6 位验证码，避免用例暴露内部随机源 */
var sixDigits = regexp.MustCompile(`\d{6}`)

/* TestNewServiceRejectsMissingDependencies 缺失依赖属于装配错误，必须拒绝创建 */
func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	if _, err := NewService(Deps{}); err == nil {
		t.Fatal("期望缺少依赖时报错，实际成功")
	}
}

/*
 * TestSendCodeDeliversAndBindsHash 验证邮件投递、邮箱归一化与摘要绑定。
 * 摘要必须同时绑定邮箱、用途与验证码，否则跨场景可复用同一验证码。
 */
func TestSendCodeDeliversAndBindsHash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "zhangsan@example.com"

	err := f.service.SendCode(ctx, SendCodeInput{Email: " ZhangSan@Example.com ", Purpose: "register"})
	if err != nil {
		t.Fatalf("发送验证码失败: %v", err)
	}
	if len(f.mail.sent) != 1 {
		t.Fatalf("期望投递 1 封邮件，实际 %d", len(f.mail.sent))
	}
	if f.mail.sent[0].To != email {
		t.Fatalf("邮件收件人应为归一化邮箱，实际 %q", f.mail.sent[0].To)
	}

	matches := sixDigits.FindString(f.mail.sent[0].Body)
	if matches == "" {
		t.Fatalf("邮件正文缺少 6 位验证码: %q", f.mail.sent[0].Body)
	}
	record, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister)
	if err != nil {
		t.Fatalf("读取验证码记录: %v", err)
	}
	if want := f.digester.Digest(email, entity.PurposeRegister, matches); record.CodeHash != want {
		t.Fatal("验证码摘要未绑定邮箱、用途与验证码")
	}
}

/* TestSendCodeRateLimitsAndInvalidatesPrevious 校验 60 秒窗口与旧码失效 */
func TestSendCodeRateLimitsAndInvalidatesPrevious(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "lisi@example.com"
	input := SendCodeInput{Email: email, Purpose: entity.PurposeRegister}

	if err := f.service.SendCode(ctx, input); err != nil {
		t.Fatalf("首次发送失败: %v", err)
	}
	first, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister)
	if err != nil {
		t.Fatalf("读取首条验证码: %v", err)
	}

	limited := requireCode(t, f.service.SendCode(ctx, input), errs.CodeAuthCodeRateLimited)
	if limited.Meta()["retryAfterSeconds"] == nil {
		t.Fatal("频率限制必须携带 retryAfterSeconds")
	}

	f.clock.Advance(CodeSendWindow)
	if err := f.service.SendCode(ctx, input); err != nil {
		t.Fatalf("窗口结束后应允许再次发送: %v", err)
	}
	second, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister)
	if err != nil {
		t.Fatalf("读取新验证码: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("新验证码应替换旧记录为最新可用记录")
	}
}

/* TestSendCodeDailyLimit 验证每日 10 次的独立窗口 */
func TestSendCodeDailyLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "wangwu@example.com"
	identity := f.digester.Digest(email)

	for index := 0; index < CodeDailyLimit; index++ {
		allowed, _, err := f.limiter.Take(ctx, "rl:code:day:"+identity, CodeDailyLimit, CodeDailyWindow)
		if err != nil || !allowed {
			t.Fatalf("预占每日配额失败: allowed=%v err=%v", allowed, err)
		}
	}
	err := f.service.SendCode(ctx, SendCodeInput{Email: email, Purpose: entity.PurposeRegister})
	requireCode(t, err, errs.CodeAuthCodeRateLimited)
}

/* TestSendCodeDoesNotRevealAccountState 注册码与重置码对任何邮箱都返回同一结果，避免枚举 */
func TestSendCodeDoesNotRevealAccountState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "zhaoliu@example.com"
	f.seedUser(t, email, "pass1234")

	if err := f.service.SendCode(ctx, SendCodeInput{Email: email, Purpose: entity.PurposeRegister}); err != nil {
		t.Fatalf("已注册邮箱申请注册码应返回成功: %v", err)
	}
	if err := f.service.SendCode(ctx, SendCodeInput{Email: "nobody@example.com", Purpose: entity.PurposeResetPassword}); err != nil {
		t.Fatalf("未注册邮箱申请重置码应返回成功: %v", err)
	}
	requireCode(t, f.service.SendCode(ctx, SendCodeInput{Email: email, Purpose: "UNKNOWN"}),
		errs.CodeValidationFailed)
	requireCode(t, f.service.SendCode(ctx, SendCodeInput{Email: "bad-email", Purpose: entity.PurposeRegister}),
		errs.CodeValidationFailed)
}

/* TestRegisterCreatesAccountRootFolderAndTokens 覆盖注册的全部副作用 */
func TestRegisterCreatesAccountRootFolderAndTokens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "sunqi@example.com"

	session := f.register(t, email, "pass1234")
	if session.TokenType != "Bearer" || session.ExpiresIn != 900 {
		t.Fatalf("令牌响应字段异常: %+v", session)
	}
	if session.User.Email != email || session.User.Nickname != "sunqi" || !session.User.EmailVerified {
		t.Fatalf("用户视图异常: %+v", session.User)
	}

	claims, err := f.issuer.Verify(session.AccessToken, f.clock.Now())
	if err != nil {
		t.Fatalf("校验访问令牌: %v", err)
	}
	if claims.UserID != session.User.ID {
		t.Fatalf("访问令牌主体应为用户 ID，实际 %q", claims.UserID)
	}

	stored, err := f.tokens.GetByHash(ctx, hashRefreshToken(session.RefreshToken))
	if err != nil {
		t.Fatalf("读取刷新令牌: %v", err)
	}
	if stored.UserID != session.User.ID || !stored.Usable(f.clock.Now()) {
		t.Fatalf("刷新令牌状态异常: %+v", stored)
	}

	if len(f.folders.created) != 1 {
		t.Fatalf("注册必须创建 1 个根目录，实际 %d", len(f.folders.created))
	}
	root := f.folders.created[0]
	if root.Name != entity.RootFolderName || root.Depth != 1 || root.ParentID != nil {
		t.Fatalf("根目录字段异常: %+v", root)
	}
	if root.Path != "/"+root.ID || root.UserID != session.User.ID {
		t.Fatalf("根目录归属或路径异常: %+v", root)
	}

	user, err := f.users.GetByEmail(ctx, email)
	if err != nil {
		t.Fatalf("读取账号: %v", err)
	}
	if user.PasswordHash != "stub:pass1234" {
		t.Fatalf("落库口令应为端口产出的编码，实际 %q", user.PasswordHash)
	}
	if _, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister); !isNotFound(err) {
		t.Fatalf("注册成功后验证码应被消费，实际 err=%v", err)
	}
}

/* TestRegisterRequiresAgreements 未勾选协议必须拒绝且不写账号 */
func TestRegisterRequiresAgreements(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "zhouba@example.com"
	f.seedCode(t, email, entity.PurposeRegister, "123456")

	_, err := f.service.Register(ctx, RegisterInput{
		Email: email, Code: "123456", Password: "pass1234",
		Agreements: Agreements{UserAgreement: true},
	})
	requireCode(t, err, errs.CodeAuthAgreementRequired)
	if _, err := f.users.GetByEmail(ctx, email); !isNotFound(err) {
		t.Fatalf("注册被拒绝后不应创建账号，实际 err=%v", err)
	}
}

/* TestRegisterCountsWrongCodeAttempts 验证错误次数落库与作废规则 */
func TestRegisterCountsWrongCodeAttempts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "wujiu@example.com"
	f.seedCode(t, email, entity.PurposeRegister, "123456")
	input := RegisterInput{
		Email: email, Code: "000000", Password: "pass1234",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	}

	before := f.hasher.hashCalls
	requireCode(t, sessionErr(f.service.Register(ctx, input)), errs.CodeAuthCodeInvalid)
	/* 验证码错误必须在 Argon2id 之前被拒绝，不能触发口令哈希 */
	if f.hasher.hashCalls != before {
		t.Fatalf("验证码错误时不应执行口令哈希，调用次数 %d → %d", before, f.hasher.hashCalls)
	}
	record, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister)
	if err != nil {
		t.Fatalf("读取验证码记录: %v", err)
	}
	if record.Attempts != 1 {
		t.Fatalf("错误次数必须提交，期望 1 实际 %d", record.Attempts)
	}

	/* 连续错误达到上限后记录作废，不再可用 */
	for attempt := 1; attempt < entity.MaxCodeAttempts; attempt++ {
		requireCode(t, sessionErr(f.service.Register(ctx, input)), errs.CodeAuthCodeInvalid)
	}
	if _, err := f.codes.LatestActive(ctx, email, entity.PurposeRegister); !isNotFound(err) {
		t.Fatalf("达到错误上限后验证码应作废，实际 err=%v", err)
	}
}

/* TestRegisterRejectsExpiredCode 验证 10 分钟有效期 */
func TestRegisterRejectsExpiredCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "zhengshi@example.com"
	f.seedCode(t, email, entity.PurposeRegister, "123456")
	f.clock.Advance(VerificationCodeTTL + time.Second)

	_, err := f.service.Register(ctx, RegisterInput{
		Email: email, Code: "123456", Password: "pass1234",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	requireCode(t, err, errs.CodeAuthCodeExpired)
}

/* TestRegisterRejectsDuplicateAndWeakPassword 覆盖邮箱占用与密码策略 */
func TestRegisterRejectsDuplicateAndWeakPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "chenyi@example.com"
	f.seedUser(t, email, "pass1234")
	/* 先通过验证码预校验，才能走到邮箱占用判定 */
	f.seedCode(t, email, entity.PurposeRegister, "123456")

	_, err := f.service.Register(ctx, RegisterInput{
		Email: email, Code: "123456", Password: "pass1234",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	requireCode(t, err, errs.CodeAuthEmailAlreadyExists)

	_, err = f.service.Register(ctx, RegisterInput{
		Email: "new@example.com", Code: "123456", Password: "short",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	requireCode(t, err, errs.CodeValidationFailed)
}

/* TestLoginLocksAfterConsecutiveFailures 验证 5 次失败锁定 15 分钟 */
func TestLoginLocksAfterConsecutiveFailures(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "songer@example.com"
	user := f.seedUser(t, email, "pass1234")
	bad := LoginInput{Email: email, Password: "wrong1234"}

	for attempt := 0; attempt < LoginFailureLimit-1; attempt++ {
		requireCode(t, sessionErr(f.service.Login(ctx, bad)), errs.CodeAuthInvalidCredentials)
	}
	locked := requireCode(t, sessionErr(f.service.Login(ctx, bad)), errs.CodeAuthTooManyAttempts)
	if locked.Meta()["retryAfterSeconds"] == nil {
		t.Fatal("锁定响应必须携带 retryAfterSeconds")
	}

	/* 锁定期内即使口令正确也必须拒绝，且不产生新令牌 */
	requireCode(t, sessionErr(f.service.Login(ctx, LoginInput{Email: email, Password: "pass1234"})),
		errs.CodeAuthTooManyAttempts)

	f.clock.Advance(LoginLockDuration)
	session, err := f.service.Login(ctx, LoginInput{Email: email, Password: "pass1234"})
	if err != nil {
		t.Fatalf("锁定解除后应允许登录: %v", err)
	}
	if session.User.ID != user.ID {
		t.Fatalf("登录用户不匹配: %+v", session.User)
	}
}

/* TestLoginUnknownEmailMatchesWrongPassword 未注册邮箱与口令错误返回同一错误码，避免账号枚举 */
func TestLoginUnknownEmailMatchesWrongPassword(t *testing.T) {
	f := newFixture(t)
	requireCode(t, sessionErr(f.service.Login(context.Background(), LoginInput{
		Email: "nobody@example.com", Password: "pass1234",
	})), errs.CodeAuthInvalidCredentials)
}

/* TestLoginLockIsBoundToClientIP 失败计数绑定来源，攻击者无法用错误口令锁死他人账号 */
func TestLoginLockIsBoundToClientIP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "victim@example.com"
	f.seedUser(t, email, "pass1234")

	attacker := LoginInput{Email: email, Password: "wrong1234", ClientIP: "203.0.113.9"}
	for attempt := 0; attempt < LoginFailureLimit-1; attempt++ {
		requireCode(t, sessionErr(f.service.Login(ctx, attacker)), errs.CodeAuthInvalidCredentials)
	}
	requireCode(t, sessionErr(f.service.Login(ctx, attacker)), errs.CodeAuthTooManyAttempts)

	/* 受害者从其他来源用正确口令登录，不应受攻击者计数影响 */
	session, err := f.service.Login(ctx, LoginInput{Email: email, Password: "pass1234", ClientIP: "198.51.100.7"})
	if err != nil {
		t.Fatalf("其他来源的正确口令应可登录: %v", err)
	}
	if session.User.Email != email {
		t.Fatalf("登录用户不匹配: %+v", session.User)
	}
}

/* TestRefreshRotatesAndDetectsReplay 验证轮换、重放检测与按设备吊销 */
func TestRefreshRotatesAndDetectsReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.register(t, "zhangsan@example.com", "pass1234")

	second, err := f.service.Refresh(ctx, RefreshInput{RefreshToken: first.RefreshToken})
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("刷新必须轮换出新令牌")
	}
	rotated, err := f.tokens.GetByHash(ctx, hashRefreshToken(first.RefreshToken))
	if err != nil {
		t.Fatalf("读取旧令牌: %v", err)
	}
	if rotated.ReplacedBy == "" || rotated.RevokedAt == nil {
		t.Fatalf("旧令牌应记录替换关系并吊销: %+v", rotated)
	}

	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: first.RefreshToken})),
		errs.CodeAuthRefreshReused)
	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: second.RefreshToken})),
		errs.CodeAuthTokenInvalid)
}

/* TestRefreshConcurrentOnlyOneSucceeds 验证并发刷新同一令牌时只有一个成功，其余按重放拒绝 */
func TestRefreshConcurrentOnlyOneSucceeds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.register(t, "concurrent@example.com", "pass1234")

	const attempts = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, attempts)
	wg.Add(attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := f.service.Refresh(ctx, RefreshInput{RefreshToken: first.RefreshToken})
			results[index] = err
		}(index)
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errs.IsCode(err, errs.CodeAuthRefreshReused) {
			t.Fatalf("并发刷新失败应为重放错误: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("并发刷新成功次数 = %d，期望 1", successes)
	}
}

/* TestRefreshRejectsExpiredAndUnknown 覆盖过期与未知令牌 */
func TestRefreshRejectsExpiredAndUnknown(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	session := f.register(t, "lisi@example.com", "pass1234")

	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: "not-a-token"})),
		errs.CodeAuthTokenInvalid)
	f.clock.Advance(RefreshTokenTTL + time.Second)
	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: session.RefreshToken})),
		errs.CodeAuthTokenInvalid)
}

/* TestLogoutRevokesCurrentOrAllDevices 验证黑名单与按设备/全量吊销 */
func TestLogoutRevokesCurrentOrAllDevices(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "wangwu@example.com"
	f.seedCode(t, email, entity.PurposeRegister, "123456")

	first, err := f.service.Register(ctx, RegisterInput{
		Email: email, Code: "123456", Password: "pass1234", DeviceID: "dev-a",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	second, err := f.service.Login(ctx, LoginInput{Email: email, Password: "pass1234", DeviceID: "dev-b"})
	if err != nil {
		t.Fatalf("第二设备登录失败: %v", err)
	}

	err = f.service.Logout(ctx, LogoutInput{
		UserID: first.User.ID, TokenID: "jti-1",
		AccessExpiresAt: f.clock.Now().Add(15 * time.Minute),
		RefreshToken:    first.RefreshToken,
	})
	if err != nil {
		t.Fatalf("退出登录失败: %v", err)
	}
	if denied, err := f.denylist.Contains(ctx, "jti-1"); err != nil || !denied {
		t.Fatalf("访问令牌应进入黑名单: denied=%v err=%v", denied, err)
	}

	revoked, err := f.tokens.GetByHash(ctx, hashRefreshToken(first.RefreshToken))
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("当前设备刷新令牌应被吊销: %+v err=%v", revoked, err)
	}
	alive, err := f.tokens.GetByHash(ctx, hashRefreshToken(second.RefreshToken))
	if err != nil || alive.RevokedAt != nil {
		t.Fatalf("其他设备令牌不应受影响: %+v err=%v", alive, err)
	}

	if err := f.service.Logout(ctx, LogoutInput{UserID: first.User.ID, AllDevices: true}); err != nil {
		t.Fatalf("全设备退出失败: %v", err)
	}
	alive, err = f.tokens.GetByHash(ctx, hashRefreshToken(second.RefreshToken))
	if err != nil || alive.RevokedAt == nil {
		t.Fatalf("全设备退出应吊销其他设备令牌: %+v err=%v", alive, err)
	}
}

/* TestResetPasswordRotatesCredentials 验证口令更换与全部令牌吊销 */
func TestResetPasswordRotatesCredentials(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "zhaoliu@example.com"
	registered := f.register(t, email, "oldpass123")
	loggedIn, err := f.service.Login(ctx, LoginInput{Email: email, Password: "oldpass123"})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	f.seedCode(t, email, entity.PurposeResetPassword, "654321")

	err = f.service.ResetPassword(ctx, ResetPasswordInput{
		Email: email, Code: "654321", NewPassword: "newpass456",
	})
	if err != nil {
		t.Fatalf("重置密码失败: %v", err)
	}

	user, err := f.users.GetByEmail(ctx, email)
	if err != nil {
		t.Fatalf("读取账号: %v", err)
	}
	if user.PasswordHash != "stub:newpass456" {
		t.Fatalf("口令未更新: %q", user.PasswordHash)
	}
	requireCode(t, sessionErr(f.service.Login(ctx, LoginInput{Email: email, Password: "oldpass123"})),
		errs.CodeAuthInvalidCredentials)
	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: registered.RefreshToken})),
		errs.CodeAuthTokenInvalid)
	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: loggedIn.RefreshToken})),
		errs.CodeAuthTokenInvalid)
	if _, err := f.service.Login(ctx, LoginInput{Email: email, Password: "newpass456"}); err != nil {
		t.Fatalf("新口令应可登录: %v", err)
	}
}

/* TestResetPasswordRejectsWrongCode 错误验证码不改变口令 */
func TestResetPasswordRejectsWrongCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "qianba@example.com"
	f.register(t, email, "oldpass123")
	f.seedCode(t, email, entity.PurposeResetPassword, "654321")

	before := f.hasher.hashCalls
	requireCode(t, f.service.ResetPassword(ctx, ResetPasswordInput{
		Email: email, Code: "000000", NewPassword: "newpass456",
	}), errs.CodeAuthCodeInvalid)
	/* 验证码错误必须在 Argon2id 之前被拒绝，不能触发口令哈希 */
	if f.hasher.hashCalls != before {
		t.Fatalf("验证码错误时不应执行口令哈希，调用次数 %d → %d", before, f.hasher.hashCalls)
	}
	user, err := f.users.GetByEmail(ctx, email)
	if err != nil {
		t.Fatalf("读取账号: %v", err)
	}
	if user.PasswordHash != "stub:oldpass123" {
		t.Fatalf("验证码错误时不得修改口令: %q", user.PasswordHash)
	}
}

/* TestAccountProfileAndDeletion 覆盖资料读取、昵称修改与注销级联 */
func TestAccountProfileAndDeletion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	email := "sunqi@example.com"
	session := f.register(t, email, "pass1234")

	view, err := f.service.GetUser(ctx, session.User.ID)
	if err != nil {
		t.Fatalf("读取当前用户: %v", err)
	}
	if view.Email != email || view.Nickname != "sunqi" {
		t.Fatalf("用户视图异常: %+v", view)
	}

	updated, err := f.service.UpdateNickname(ctx, session.User.ID, "  新昵称 ")
	if err != nil {
		t.Fatalf("修改昵称失败: %v", err)
	}
	if updated.Nickname != "新昵称" {
		t.Fatalf("昵称应去除首尾空白，实际 %q", updated.Nickname)
	}
	_, err = f.service.UpdateNickname(ctx, session.User.ID, "   ")
	requireCode(t, err, errs.CodeValidationFailed)

	requireCode(t, f.service.DeleteAccount(ctx, DeleteAccountInput{
		UserID: session.User.ID, Code: "123456", Confirm: false,
	}), errs.CodeValidationFailed)

	f.seedCode(t, email, entity.PurposeDeleteAccount, "112233")
	requireCode(t, f.service.DeleteAccount(ctx, DeleteAccountInput{
		UserID: session.User.ID, Code: "000000", Confirm: true,
	}), errs.CodeAuthCodeInvalid)

	if err := f.service.DeleteAccount(ctx, DeleteAccountInput{
		UserID: session.User.ID, Code: "112233", Confirm: true,
	}); err != nil {
		t.Fatalf("注销失败: %v", err)
	}
	if _, err := f.service.GetUser(ctx, session.User.ID); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("注销后账号应不可读，实际 err=%v", err)
	}
	if len(f.account.purged) != 1 || f.account.purged[0] != session.User.ID {
		t.Fatalf("注销必须清理账号全部业务数据: %v", f.account.purged)
	}
	requireCode(t, sessionErr(f.service.Refresh(ctx, RefreshInput{RefreshToken: session.RefreshToken})),
		errs.CodeAuthTokenInvalid)
}
