/* CORS 中间件的行为：通配符放行、显式来源回显、预检 204、白名单外不写头 */
package httptransport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

/* newCORSEngine 用最小路由复现装配顺序：CORS 在路由之前 */
func newCORSEngine(origins []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(cors(origins))
	engine.POST("/v1/auth:register", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return engine
}

/* TestCORSPreflightWildcard 校验通配符下的预检响应与缓存头 */
func TestCORSPreflightWildcard(t *testing.T) {
	engine := newCORSEngine([]string{"*"})

	request := httptest.NewRequest(http.MethodOptions, "/v1/auth:register", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Method", "POST")
	record := httptest.NewRecorder()
	engine.ServeHTTP(record, request)

	if record.Code != http.StatusNoContent {
		t.Fatalf("预检状态码 = %d，期望 204", record.Code)
	}
	if got := record.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q，期望 *", got)
	}
	if got := record.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatalf("预检缺少 Allow-Methods")
	}
	if got := record.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatalf("预检缺少 Allow-Headers")
	}
}

/* TestCORSExplicitOrigin 校验显式白名单回显来源并声明凭据 */
func TestCORSExplicitOrigin(t *testing.T) {
	engine := newCORSEngine([]string{"http://localhost:5173"})

	request := httptest.NewRequest(http.MethodPost, "/v1/auth:register", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	record := httptest.NewRecorder()
	engine.ServeHTTP(record, request)

	if got := record.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Allow-Origin = %q，期望回显来源", got)
	}
	if got := record.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q，期望 true", got)
	}
}

/* TestCORSRejectsUnlistedOrigin 校验白名单外来源不会拿到 CORS 头 */
func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	engine := newCORSEngine([]string{"http://localhost:5173"})

	request := httptest.NewRequest(http.MethodPost, "/v1/auth:register", nil)
	request.Header.Set("Origin", "http://evil.example")
	record := httptest.NewRecorder()
	engine.ServeHTTP(record, request)

	if got := record.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("白名单外来源不应获得 Allow-Origin，实际 %q", got)
	}
}

/* TestCORSDisabledByEmptyList 校验未配置白名单时中间件直通且不写头 */
func TestCORSDisabledByEmptyList(t *testing.T) {
	engine := newCORSEngine(nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/auth:register", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	record := httptest.NewRecorder()
	engine.ServeHTTP(record, request)

	if record.Code != http.StatusCreated {
		t.Fatalf("直通状态码 = %d，期望 201", record.Code)
	}
	if got := record.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("未开启跨域时不应写 Allow-Origin，实际 %q", got)
	}
}
