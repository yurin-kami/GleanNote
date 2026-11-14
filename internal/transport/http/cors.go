/*
 * 跨域（CORS）中间件：让浏览器端跨源调用本服务，覆盖预检与凭据场景。
 *
 * 设计要点：
 *   - 白名单来自 GN_CORS_ORIGINS（逗号分隔），支持 "*" 全放行；未配置即关闭；
 *   - 使用 Authorization 头的 Bearer 鉴权，因此显式来源时允许凭据回显；
 *   - 预检请求在限流与路由匹配之前以 204 结束，避免 OPTIONS 被计入限流或被 404 拦截。
 * 参考：docs/API.md#23-请求与响应头
 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

/* corsAllowedMethods 覆盖业务实际使用的动词；OPTIONS 由中间件直接响应 */
const corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

/* corsAllowedHeaders 是客户端可携带的请求头；Idempotency-Key 与 If-Match 为业务必需 */
const corsAllowedHeaders = "Authorization, Content-Type, Idempotency-Key, If-Match, X-Request-Id"

/* corsExposedHeaders 让前端可读取分页、乐观锁与限流重试所需响应头 */
const corsExposedHeaders = "ETag, Location, Retry-After, X-Request-Id, Idempotency-Replayed"

/* corsMaxAge 让浏览器缓存预检结果 10 分钟，减少 OPTIONS 往返 */
const corsMaxAge = "600"

/*
 * cors 按白名单放行来源。
 * 未配置白名单时返回直通中间件，行为与接入前完全一致。
 */
func cors(origins []string) gin.HandlerFunc {
	allowAll := false
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if origin == "*" {
			allowAll = true
			continue
		}
		allowed[origin] = struct{}{}
	}
	if !allowAll && len(allowed) == 0 {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		/* 非浏览器请求不带 Origin，直接放行且不追加响应头 */
		if origin == "" {
			c.Next()
			return
		}

		_, listed := allowed[origin]
		if !allowAll && !listed {
			/* 来源不在白名单：不写 CORS 头，浏览器会按跨域策略拦截响应 */
			c.Next()
			return
		}

		c.Header("Vary", "Origin")
		/* 通配符下浏览器禁止与凭据同用，因此只有显式来源才回显原值并声明凭据 */
		if allowAll {
			c.Header("Access-Control-Allow-Origin", "*")
		} else {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Access-Control-Expose-Headers", corsExposedHeaders)

		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Allow-Methods", corsAllowedMethods)
			c.Header("Access-Control-Allow-Headers", corsAllowedHeaders)
			c.Header("Access-Control-Max-Age", corsMaxAge)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
