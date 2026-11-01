/*
 * 路由与探针测试：校验健康检查的响应结构、状态码与失败暴露口径。
 * 契约来源：docs/API.md#512-运维探针。
 */
package httptransport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	httptransport "github.com/gleannote/gleannote/internal/transport/http"
)

/* brokenVision 是探针失败夹具，用来验证 503 分支 */
type brokenVision struct{}

/* Name 返回固定名称，保持与正常夹具一致 */
func (brokenVision) Name() string { return "fake" }

/* Convert 在探针测试中不会被调用，直接返回不可用 */
func (brokenVision) Convert(ctx context.Context, pages []port.VisionPage) (*port.VisionResult, error) {
	return nil, port.ErrProviderUnavailable
}

/* Health 始终不可用，触发就绪探针的 503 分支 */
func (brokenVision) Health(ctx context.Context) error { return port.ErrProviderUnavailable }

/* probeBody 是测试侧的最小响应结构，只取关切字段 */
type probeBody struct {
	Status string            `json:"status"`
	Env    string            `json:"env"`
	Checks map[string]string `json:"checks"`
}

/* newRouter 用完整夹具装配路由，探针与业务共享同一套中间件 */
func newRouter(t *testing.T) http.Handler {
	t.Helper()
	return newTestApp(t).handler
}

/* doGet 发送一次 GET 请求并返回状态码与解析后的响应体 */
func doGet(t *testing.T, handler http.Handler, path string) (int, probeBody) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	handler.ServeHTTP(recorder, request)

	var body probeBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 %s 响应失败: %v，原文 %q", path, err, recorder.Body.String())
	}
	return recorder.Code, body
}

/* TestHealthzAlwaysOK 校验存活探针不受依赖状态影响 */
func TestHealthzAlwaysOK(t *testing.T) {
	status, body := doGet(t, newRouter(t), "/healthz")
	if status != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", status)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q，期望 ok", body.Status)
	}
}

/* TestReadyzAllOK 校验就绪探针在全部 Fake 正常时返回 200 */
func TestReadyzAllOK(t *testing.T) {
	status, body := doGet(t, newRouter(t), "/readyz")
	if status != http.StatusOK || body.Status != "ok" {
		t.Fatalf("状态码 = %d，status = %q，期望 200/ok", status, body.Status)
	}
	if body.Env != "dev" {
		t.Fatalf("env = %q，期望 dev", body.Env)
	}
	for _, key := range []string{"vision", "mailer", "objectStore"} {
		if body.Checks[key] != "ok" {
			t.Fatalf("checks[%s] = %q，期望 ok", key, body.Checks[key])
		}
	}
}

/* TestReadyzReportsFailure 校验依赖不可用时返回 503，且只给出固定文案不泄漏底层错误 */
func TestReadyzReportsFailure(t *testing.T) {
	app := newTestApp(t)
	deps := app.deps
	deps.Vision = brokenVision{}
	router, err := httptransport.NewRouter(deps)
	if err != nil {
		t.Fatalf("装配路由: %v", err)
	}

	status, body := doGet(t, router, "/readyz")
	if status != http.StatusServiceUnavailable || body.Status != "unavailable" {
		t.Fatalf("状态码 = %d，status = %q，期望 503/unavailable", status, body.Status)
	}
	if body.Checks["vision"] != "unavailable" {
		t.Fatalf("checks[vision] = %q，期望固定文案 unavailable", body.Checks["vision"])
	}
}
