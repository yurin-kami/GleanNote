/* 限流配额可配置：注入的配额生效，零值回填生产默认值 */
package httptransport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* newLimitedEngine 构造只带全局限流的引擎，便于断言配额边界 */
func newLimitedEngine(allowance int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	limiter := memory.NewRateLimiter(clock.NewFixed(time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)))
	engine := gin.New()
	engine.Use(globalRateLimit(limiter, allowance))
	engine.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	return engine
}

/* TestGlobalRateLimitUsesConfiguredAllowance 校验注入的配额生效 */
func TestGlobalRateLimitUsesConfiguredAllowance(t *testing.T) {
	engine := newLimitedEngine(2)
	for index := 1; index <= 2; index++ {
		record := httptest.NewRecorder()
		engine.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/ping", nil))
		if record.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态 = %d，期望 200", index, record.Code)
		}
	}
	record := httptest.NewRecorder()
	engine.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if record.Code != http.StatusTooManyRequests {
		t.Fatalf("超限状态 = %d，期望 429", record.Code)
	}
}

/* TestRateLimitsNormalizeFillsDefaults 校验零值与负数都回填生产默认值 */
func TestRateLimitsNormalizeFillsDefaults(t *testing.T) {
	limits := RateLimits{GlobalPerMinute: -1}.normalize()
	if limits.GlobalPerMinute != defaultGlobalPerMinute ||
		limits.LoginPerMinute != defaultLoginPerMinute ||
		limits.WritePerMinute != defaultWritePerMinute ||
		limits.UploadPerHour != defaultUploadPerHour ||
		limits.PublicSharePerMinute != defaultPublicSharePerMinute {
		t.Fatalf("归一结果 = %+v，期望全部回填默认值", limits)
	}
}
