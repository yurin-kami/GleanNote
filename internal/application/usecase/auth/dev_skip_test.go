/* dev 跳过验证码校验的用例行为：开关打开时无需种子验证码即可注册，关闭时仍强制校验 */
package auth

import (
	"context"
	"testing"
)

/* devSkipFixture 打开 SkipCodeVerification，模拟 bootstrap 在 GN_ENV=dev 下的装配 */
func devSkipFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.service.deps.SkipCodeVerification = true
	return f
}

/* TestRegisterWithoutCodeInDev 校验跳过校验时可用空验证码完成注册并拿到登录态 */
func TestRegisterWithoutCodeInDev(t *testing.T) {
	f := devSkipFixture(t)

	session, err := f.service.Register(context.Background(), RegisterInput{
		Email:      "dev@example.com",
		Code:       "",
		Password:   "devpass123",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	})
	if err != nil {
		t.Fatalf("dev 跳过校验时注册应成功，实际错误: %v", err)
	}
	if session.User.Email != "dev@example.com" {
		t.Fatalf("注册邮箱 = %q，期望 dev@example.com", session.User.Email)
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		t.Fatalf("注册应同时签发访问令牌与刷新令牌")
	}
}

/* TestRegisterRequiresCodeOutsideDev 确认开关关闭时行为不变：空验证码仍被拒绝 */
func TestRegisterRequiresCodeOutsideDev(t *testing.T) {
	f := newFixture(t)

	if _, err := f.service.Register(context.Background(), RegisterInput{
		Email:      "nodev@example.com",
		Code:       "",
		Password:   "devpass123",
		Agreements: Agreements{UserAgreement: true, PrivacyPolicy: true},
	}); err == nil {
		t.Fatalf("未开启跳过校验时，空验证码应被拒绝")
	}
}
