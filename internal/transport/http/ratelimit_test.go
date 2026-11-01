/* 限流中间件测试：全局限流按真实 IP 计数，且不信任可伪造的代理头 */
package httptransport_test

import (
	"fmt"
	"net/http"
	"testing"
)

/* TestPublicShareRateLimitByIP 验证匿名公开读取按 IP 限流，且伪造代理头无效 */
func TestPublicShareRateLimitByIP(t *testing.T) {
	app := newTestApp(t)
	const allowance = 60

	/* 未知令牌返回 410，但前 60 次不应被限流 */
	for index := 0; index < allowance; index++ {
		result := app.do(t, http.MethodGet, "/v1/public/shares/unknown-token", "",
			map[string]string{"X-Forwarded-For": fmt.Sprintf("10.0.%d.%d", index/256, index%256)})
		if result.status == http.StatusTooManyRequests {
			t.Fatalf("第 %d 次公开读取不应被限流", index+1)
		}
	}

	blocked := app.do(t, http.MethodGet, "/v1/public/shares/unknown-token", "",
		map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if blocked.status != http.StatusTooManyRequests {
		t.Fatalf("公开读取超限状态 = %d，期望 429", blocked.status)
	}
	if decodeProblem(t, blocked).Code != "RATE_LIMITED" {
		t.Fatalf("超限业务码不符: %s", string(blocked.body))
	}
}

/* TestGlobalRateLimitIgnoresForwardedHeaders 验证伪造 X-Forwarded-For 无法绕过全局单 IP 限流 */
func TestGlobalRateLimitIgnoresForwardedHeaders(t *testing.T) {
	app := newTestApp(t)
	const allowance = 600

	/* 前 600 次即使每次更换伪造来源头也应放行 */
	for index := 0; index < allowance; index++ {
		result := app.do(t, http.MethodGet, "/healthz", "",
			map[string]string{"X-Forwarded-For": fmt.Sprintf("10.0.%d.%d", index/256, index%256)})
		if result.status != http.StatusOK {
			t.Fatalf("第 %d 次请求状态 = %d，期望 200", index+1, result.status)
		}
	}

	/* 第 601 次仍来自同一真实地址，必须被限流，且给出重试建议 */
	blocked := app.do(t, http.MethodGet, "/healthz", "",
		map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if blocked.status != http.StatusTooManyRequests {
		t.Fatalf("超限状态 = %d，期望 429", blocked.status)
	}
	if blocked.header.Get("Retry-After") == "" {
		t.Fatal("缺少 Retry-After 响应头")
	}
	if decodeProblem(t, blocked).Code != "RATE_LIMITED" {
		t.Fatalf("超限业务码不符: %s", string(blocked.body))
	}
}
