/*
 * 认证与账号端点的协议测试：覆盖状态码、错误码、幂等与令牌失效口径。
 * 契约来源：docs/API.md#41-账号与认证、docs/API.md#28-错误模型。
 */
package httptransport_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* TestSendCodeRejectsInvalidEmail 同时验证冒号路径改写生效，且 instance 保留契约路径 */
func TestSendCodeRejectsInvalidEmail(t *testing.T) {
	app := newTestApp(t)
	result := app.do(t, http.MethodPost, "/v1/auth:sendCode",
		`{"email":"not-an-email","purpose":"REGISTER"}`, nil)
	if result.status != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400，原文 %q", result.status, string(result.body))
	}

	problem := decodeProblem(t, result)
	if problem.Code != "VALIDATION_FAILED" {
		t.Fatalf("code = %q，期望 VALIDATION_FAILED", problem.Code)
	}
	if !strings.HasPrefix(problem.Instance, "/v1/auth:sendCode#") {
		t.Fatalf("instance = %q，期望保留冒号路径并附带请求 ID", problem.Instance)
	}
	if want := result.header.Get("X-Request-Id"); want == "" || problem.RequestID != want {
		t.Fatalf("requestId = %q，响应头 = %q，期望一致且非空", problem.RequestID, want)
	}
}

/* TestRegisterReplayReturnsFirstResponse 校验同键同体回放，且不重复创建账号 */
func TestRegisterReplayReturnsFirstResponse(t *testing.T) {
	app := newTestApp(t)
	app.seedCode(t, "alice@example.com", entity.PurposeRegister, "123456")
	payload := `{"email":"alice@example.com","code":"123456","password":"Password1","agreements":{"userAgreement":true,"privacyPolicy":true}}`
	headers := map[string]string{"Idempotency-Key": "register-key-1"}

	first := app.do(t, http.MethodPost, "/v1/auth:register", payload, headers)
	if first.status != http.StatusCreated {
		t.Fatalf("首次注册状态码 = %d，期望 201，原文 %q", first.status, string(first.body))
	}
	second := app.do(t, http.MethodPost, "/v1/auth:register", payload, headers)
	if second.status != http.StatusCreated || !bytes.Equal(first.body, second.body) {
		t.Fatalf("重放结果不一致：状态 %d，响应 %q", second.status, string(second.body))
	}
	if session := decodeSession(t, second); session.User.ID == "" {
		t.Fatalf("重放响应缺少用户 ID")
	}

	/* 同一键换请求体必须 409，防止客户端误以为复用了旧结果 */
	conflict := app.do(t, http.MethodPost, "/v1/auth:register",
		`{"email":"bob@example.com","code":"123456","password":"Password1","agreements":{"userAgreement":true,"privacyPolicy":true}}`,
		headers)
	if conflict.status != http.StatusConflict || decodeProblem(t, conflict).Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("冲突响应 = %d %q，期望 409 IDEMPOTENCY_KEY_REUSED", conflict.status, string(conflict.body))
	}
}

/* TestRegisterRequiresIdempotencyKey 校验创建类接口缺失键时返回 400 */
func TestRegisterRequiresIdempotencyKey(t *testing.T) {
	app := newTestApp(t)
	result := app.do(t, http.MethodPost, "/v1/auth:register",
		`{"email":"alice@example.com","code":"123456","password":"Password1","agreements":{"userAgreement":true,"privacyPolicy":true}}`, nil)
	if result.status != http.StatusBadRequest || decodeProblem(t, result).Code != "VALIDATION_FAILED" {
		t.Fatalf("响应 = %d %q，期望 400 VALIDATION_FAILED", result.status, string(result.body))
	}
}

/* TestLoginLocksAfterRepeatedFailures 校验连续失败 5 次后锁定并返回 Retry-After */
func TestLoginLocksAfterRepeatedFailures(t *testing.T) {
	app := newTestApp(t)
	app.registerSession(t, "bob@example.com", "Password1")

	wrong := `{"email":"bob@example.com","password":"WrongPass1"}`
	for attempt := 1; attempt <= 4; attempt++ {
		result := app.do(t, http.MethodPost, "/v1/auth:login", wrong, nil)
		if result.status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败状态码 = %d，期望 401", attempt, result.status)
		}
	}

	locked := app.do(t, http.MethodPost, "/v1/auth:login", wrong, nil)
	if locked.status != http.StatusTooManyRequests {
		t.Fatalf("第 5 次状态码 = %d，期望 429", locked.status)
	}
	problem := decodeProblem(t, locked)
	if problem.Code != "AUTH_TOO_MANY_ATTEMPTS" || problem.RetryAfterSeconds <= 0 {
		t.Fatalf("code = %q，retryAfter = %d，期望锁定并给出秒数", problem.Code, problem.RetryAfterSeconds)
	}
	if locked.header.Get("Retry-After") == "" {
		t.Fatalf("缺少 Retry-After 响应头")
	}

	/* 锁定期内即便口令正确也必须拒绝，避免被当作口令探测放大器 */
	correct := `{"email":"bob@example.com","password":"Password1"}`
	if result := app.do(t, http.MethodPost, "/v1/auth:login", correct, nil); result.status != http.StatusTooManyRequests {
		t.Fatalf("锁定期内正确口令状态码 = %d，期望 429", result.status)
	}
}

/* TestProtectedEndpointsRejectInvalidTokens 校验缺失、格式错误与过期三类令牌 */
func TestProtectedEndpointsRejectInvalidTokens(t *testing.T) {
	app := newTestApp(t)

	missing := app.do(t, http.MethodGet, "/v1/users/me", "", nil)
	if missing.status != http.StatusUnauthorized || decodeProblem(t, missing).Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("无令牌响应 = %d %q，期望 401 AUTH_TOKEN_INVALID", missing.status, string(missing.body))
	}

	malformed := app.do(t, http.MethodGet, "/v1/users/me", "", map[string]string{"Authorization": "Basic abc"})
	if malformed.status != http.StatusUnauthorized {
		t.Fatalf("非法头状态码 = %d，期望 401", malformed.status)
	}

	expired, _, err := app.issuer.Issue("id-0001", "", "jti-expired", app.clock.Now().Add(-16*time.Minute))
	if err != nil {
		t.Fatalf("签发过期令牌: %v", err)
	}
	stale := app.do(t, http.MethodGet, "/v1/users/me", "", bearer(expired))
	if stale.status != http.StatusUnauthorized || decodeProblem(t, stale).Code != "AUTH_TOKEN_EXPIRED" {
		t.Fatalf("过期令牌响应 = %d %q，期望 401 AUTH_TOKEN_EXPIRED", stale.status, string(stale.body))
	}
}

/* TestProfileUpdateAndLogout 覆盖资料读取、昵称修改与登出后令牌立即失效 */
func TestProfileUpdateAndLogout(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "carol@example.com", "Password1")

	me := app.do(t, http.MethodGet, "/v1/users/me", "", bearer(session.AccessToken))
	if me.status != http.StatusOK {
		t.Fatalf("读取资料状态码 = %d，期望 200", me.status)
	}
	profile := decodeUser(t, me)
	if profile.Email != "carol@example.com" || !profile.EmailVerified {
		t.Fatalf("资料 = %+v，期望邮箱已验证", profile)
	}
	if profile.CreatedAt != "2026-10-28T06:00:00Z" {
		t.Fatalf("createdAt = %q，期望 UTC 秒级", profile.CreatedAt)
	}

	renamed := app.do(t, http.MethodPatch, "/v1/users/me", `{"nickname":"新昵称"}`, bearer(session.AccessToken))
	if renamed.status != http.StatusOK || decodeUser(t, renamed).Nickname != "新昵称" {
		t.Fatalf("修改昵称响应 = %d %q", renamed.status, string(renamed.body))
	}

	logout := app.do(t, http.MethodPost, "/v1/auth:logout",
		fmt.Sprintf(`{"refreshToken":%q}`, session.RefreshToken), bearer(session.AccessToken))
	if logout.status != http.StatusNoContent {
		t.Fatalf("登出状态码 = %d，期望 204", logout.status)
	}

	/* 黑名单按 jti 生效，登出后同一访问令牌不可再用 */
	afterLogout := app.do(t, http.MethodGet, "/v1/users/me", "", bearer(session.AccessToken))
	if afterLogout.status != http.StatusUnauthorized || decodeProblem(t, afterLogout).Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("登出后响应 = %d %q，期望 401 AUTH_TOKEN_INVALID", afterLogout.status, string(afterLogout.body))
	}
}

/* TestRefreshRotatesAndDetectsReuse 校验令牌轮换与旧令牌重放会吊销设备登录态 */
func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "dave@example.com", "Password1")

	rotated := app.do(t, http.MethodPost, "/v1/auth:refresh",
		fmt.Sprintf(`{"refreshToken":%q}`, session.RefreshToken), nil)
	if rotated.status != http.StatusOK {
		t.Fatalf("刷新状态码 = %d，期望 200，原文 %q", rotated.status, string(rotated.body))
	}
	next := decodeSession(t, rotated)
	if next.RefreshToken == session.RefreshToken || next.AccessToken == "" {
		t.Fatalf("刷新未发生轮换")
	}

	replayed := app.do(t, http.MethodPost, "/v1/auth:refresh",
		fmt.Sprintf(`{"refreshToken":%q}`, session.RefreshToken), nil)
	if replayed.status != http.StatusUnauthorized || decodeProblem(t, replayed).Code != "AUTH_REFRESH_REUSED" {
		t.Fatalf("重放响应 = %d %q，期望 401 AUTH_REFRESH_REUSED", replayed.status, string(replayed.body))
	}
}

/* TestResetPasswordInvalidatesOldCredentials 校验重置后旧密码与旧刷新令牌全部失效 */
func TestResetPasswordInvalidatesOldCredentials(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "erin@example.com", "Password1")
	app.seedCode(t, "erin@example.com", entity.PurposeResetPassword, "654321")

	reset := app.do(t, http.MethodPost, "/v1/auth:resetPassword",
		`{"email":"erin@example.com","code":"654321","newPassword":"Password2"}`,
		map[string]string{"Idempotency-Key": "reset-key-1"})
	if reset.status != http.StatusNoContent {
		t.Fatalf("重置状态码 = %d，期望 204，原文 %q", reset.status, string(reset.body))
	}

	stale := app.do(t, http.MethodPost, "/v1/auth:refresh",
		fmt.Sprintf(`{"refreshToken":%q}`, session.RefreshToken), nil)
	if stale.status != http.StatusUnauthorized {
		t.Fatalf("重置后旧刷新令牌状态码 = %d，期望 401", stale.status)
	}

	oldLogin := app.do(t, http.MethodPost, "/v1/auth:login", `{"email":"erin@example.com","password":"Password1"}`, nil)
	if oldLogin.status != http.StatusUnauthorized || decodeProblem(t, oldLogin).Code != "AUTH_INVALID_CREDENTIALS" {
		t.Fatalf("旧密码登录 = %d %q，期望 401 AUTH_INVALID_CREDENTIALS", oldLogin.status, string(oldLogin.body))
	}
	newLogin := app.do(t, http.MethodPost, "/v1/auth:login", `{"email":"erin@example.com","password":"Password2"}`, nil)
	if newLogin.status != http.StatusOK {
		t.Fatalf("新密码登录状态码 = %d，期望 200", newLogin.status)
	}
}

/* TestDeleteAccountRemovesUser 校验验证码二次确认后账号物理删除 */
func TestDeleteAccountRemovesUser(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "frank@example.com", "Password1")
	app.seedCode(t, "frank@example.com", entity.PurposeDeleteAccount, "111111")

	deleted := app.do(t, http.MethodDelete, "/v1/users/me", `{"code":"111111","confirm":true}`, bearer(session.AccessToken))
	if deleted.status != http.StatusNoContent {
		t.Fatalf("注销状态码 = %d，期望 204，原文 %q", deleted.status, string(deleted.body))
	}

	login := app.do(t, http.MethodPost, "/v1/auth:login", `{"email":"frank@example.com","password":"Password1"}`, nil)
	if login.status != http.StatusUnauthorized || decodeProblem(t, login).Code != "AUTH_INVALID_CREDENTIALS" {
		t.Fatalf("注销后登录 = %d %q，期望 401 AUTH_INVALID_CREDENTIALS（不暴露账号是否注册）", login.status, string(login.body))
	}
}

/* userBody 是 GET/PATCH /v1/users/me 的解析结构 */
type userBody struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Nickname      string `json:"nickname"`
	EmailVerified bool   `json:"emailVerified"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

/* decodeUser 解析用户响应，失败即终止测试 */
func decodeUser(t *testing.T, result httpResult) userBody {
	t.Helper()
	if result.status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，原文 %q", result.status, string(result.body))
	}
	var body userBody
	if err := jsonUnmarshal(result.body, &body); err != nil {
		t.Fatalf("解析用户响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}
