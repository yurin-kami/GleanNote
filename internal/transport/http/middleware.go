/*
 * 传输层中间件：请求 ID、访问日志、panic 恢复、Bearer 鉴权与创建类幂等。
 *
 * 职责：把跨切面关注点集中在路由装配处，处理器只关心参数与用例调用。
 * 不负责：业务鉴权规则（锁定、令牌吊销等由应用层与端口承担）。
 * 参考：docs/API.md#23-请求与响应头、docs/API.md#26-幂等、docs/ARCHITECTURE.md#811-日志指标与追踪
 */
package httptransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/requestid"
)

/* 幂等快照有效期 24 小时，与 docs/API.md#26-幂等 一致 */
const idempotencyTTL = 24 * time.Hour

/*
 * 限流口径与 docs/ARCHITECTURE.md#87-限流与防滥用 一致。
 * 超限统一返回 429 RATE_LIMITED，并带 Retry-After；键格式 rl:{scope}:{identity}。
 */
const (
	globalRateAllowance = 600
	globalRateWindow    = time.Minute
	loginRateAllowance  = 10
	loginRateWindow     = time.Minute
	writeRateAllowance  = 120
	writeRateWindow     = time.Minute
	uploadRateAllowance = 100
	uploadRateWindow    = time.Hour
)

/*
 * 公开分享匿名读取限流口径：60 次/分钟/IP。
 * 参考：docs/API.md#59-分享链接
 */
const (
	publicShareAllowance = 60
	publicShareWindow    = time.Minute
)

/* securityHeaders 为所有响应补充基础安全头；本服务只输出 JSON，不渲染 HTML */
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.Writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Next()
	}
}

/* requestIDMiddleware 复用合法客户端 ID，否则生成；响应头始终回显，供日志与错误体关联 */
func requestIDMiddleware(now clock.Clock) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-Id"))
		if !requestid.Valid(id) {
			id = newRequestID(now)
		}
		c.Request = c.Request.WithContext(requestid.With(c.Request.Context(), id))
		c.Header("X-Request-Id", id)
		c.Next()
	}
}

/* newRequestID 在随机源异常时用时间戳兜底，保证响应头不缺失 */
func newRequestID(now clock.Clock) string {
	generated, err := requestid.New()
	if err == nil {
		return generated
	}
	return "req_" + strconv.FormatInt(now.Now().UnixNano(), 36)
}

/*
 * accessLog 记录一行结构化访问日志。
 * 约束：只记录方法与路径，不记录查询串、请求体、邮箱与令牌（G-10）。
 */
func accessLog(logger *slog.Logger, now clock.Clock) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := now.Now()
		c.Next()
		logger.Info("HTTP 请求", accessFields(c, start, now)...)
	}
}

/* accessFields 组装结构化字段；鉴权中间件在日志之后执行，因此这里再读一次上下文 */
func accessFields(c *gin.Context, start time.Time, now clock.Clock) []any {
	fields := []any{
		"method", c.Request.Method,
		"path", instancePath(c),
		"status", c.Writer.Status(),
		"latencyMs", now.Now().Sub(start).Milliseconds(),
		"requestId", requestid.From(c.Request.Context()),
	}
	if claims, ok := accessClaimsFrom(c); ok {
		fields = append(fields, "userId", claims.UserID)
	}
	return fields
}

/* recoveryMiddleware 捕获处理器 panic 并输出问题响应 */
func recoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			handlePanic(c, logger, recover())
		}()
		c.Next()
	}
}

/* handlePanic 记录堆栈并写 500；http.ErrAbortHandler 必须重抛给 net/http 处理 */
func handlePanic(c *gin.Context, logger *slog.Logger, recovered any) {
	if recovered == nil {
		return
	}
	if recovered == http.ErrAbortHandler {
		panic(recovered)
	}
	logger.Error("请求处理发生 panic",
		"requestId", requestid.From(c.Request.Context()),
		"path", instancePath(c),
		"error", recovered,
		"stack", string(debug.Stack()),
	)
	respondError(c, errs.New(errs.CodeInternal, "未预期的内部错误"))
}

/*
 * authenticate 校验 Bearer 令牌并注入登录上下文。
 * 区分口径：过期返回 AUTH_TOKEN_EXPIRED，其余（含黑名单命中）返回 AUTH_TOKEN_INVALID。
 * 参考：docs/API.md#22-认证、docs/ARCHITECTURE.md#86-认证与授权
 */
func authenticate(verifier port.AccessTokenVerifier, denylist port.AccessTokenDenylist, now clock.Clock) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := verifyBearer(c, verifier, denylist, now)
		if err != nil {
			respondError(c, err)
			return
		}
		setAccessClaims(c, claims)
		c.Next()
	}
}

/* verifyBearer 完成格式解析与签名校验；失败时返回已带业务码的错误 */
func verifyBearer(c *gin.Context, verifier port.AccessTokenVerifier, denylist port.AccessTokenDenylist, now clock.Clock) (port.AccessClaims, error) {
	raw, ok := bearerToken(c.GetHeader("Authorization"))
	if !ok {
		return port.AccessClaims{}, errs.New(errs.CodeAuthTokenInvalid, "缺少或非法的 Authorization 头")
	}
	claims, err := verifier.Verify(raw, now.Now())
	if err != nil {
		return port.AccessClaims{}, tokenError(err)
	}

	/* 登出后的令牌必须立即失效；黑名单只存 jti，TTL 至令牌原过期时刻 */
	denied, err := denylist.Contains(c.Request.Context(), claims.TokenID)
	if err != nil {
		return port.AccessClaims{}, errs.New(errs.CodeInternal, "查询令牌黑名单失败").WithCause(err)
	}
	if denied {
		return port.AccessClaims{}, errs.New(errs.CodeAuthTokenInvalid, "访问令牌已登出")
	}
	return claims, nil
}

/* bearerToken 解析 Bearer 前缀；前缀大小写不敏感，令牌本体不能为空 */
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	raw := strings.TrimSpace(header[len(prefix):])
	return raw, raw != ""
}

/* tokenError 把校验失败细分为过期与非法两类，客户端据此决定是否刷新 */
func tokenError(err error) error {
	if errors.Is(err, port.ErrTokenExpired) {
		return errs.New(errs.CodeAuthTokenExpired, "访问令牌已过期")
	}
	return errs.New(errs.CodeAuthTokenInvalid, "访问令牌无效").WithCause(err)
}

/*
 * publicShareRateLimit 限制匿名公开读取频率，键为客户端 IP。
 * 超限返回 429 并携带建议重试秒数；限流器异常属于内部错误，不静默放行。
 * 参考：docs/API.md#59-分享链接
 */
func publicShareRateLimit(limiter port.RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, wait, err := limiter.Take(c.Request.Context(),
			"rl:public-share:"+c.ClientIP(), publicShareAllowance, publicShareWindow)
		if err != nil {
			respondError(c, errs.New(errs.CodeInternal, "公开分享限流校验失败").WithCause(err))
			return
		}
		if !allowed {
			respondError(c, rateLimitedError(wait))
			return
		}
		c.Next()
	}
}

/* rateLimitedError 把剩余等待时间写入元数据，统一复用 retryAfterSeconds 的取整口径 */
func rateLimitedError(wait time.Duration) error {
	return errs.New(errs.CodeRateLimited, "公开分享读取过于频繁").
		WithMeta("retryAfterSeconds", retryAfterSeconds(wait))
}

/*
 * globalRateLimit 限制单 IP 每分钟请求总数，抵御撞库与资源耗尽。
 * 键基于 c.ClientIP()，因此路由必须禁用代理头信任（见 router.go 的 SetTrustedProxies）。
 */
func globalRateLimit(limiter port.RateLimiter) gin.HandlerFunc {
	return rateLimit(limiter, func(c *gin.Context) string {
		return "rl:global:" + c.ClientIP()
	}, globalRateAllowance, globalRateWindow, "请求过于频繁")
}

/* loginRateLimit 限制单 IP 登录尝试频率，与账号级失败锁定互补 */
func loginRateLimit(limiter port.RateLimiter) gin.HandlerFunc {
	return rateLimit(limiter, func(c *gin.Context) string {
		return "rl:login:" + c.ClientIP()
	}, loginRateAllowance, loginRateWindow, "登录尝试过于频繁")
}

/* writeRateLimit 限制单用户写操作频率；读请求不消耗写配额 */
func writeRateLimit(limiter port.RateLimiter) gin.HandlerFunc {
	return rateLimit(limiter, func(c *gin.Context) string {
		if safeMethod(c.Request.Method) {
			return ""
		}
		claims, ok := accessClaimsFrom(c)
		if !ok || claims.UserID == "" {
			return ""
		}
		return "rl:write:" + claims.UserID
	}, writeRateAllowance, writeRateWindow, "写操作过于频繁")
}

/* safeMethod 判断只读方法，限流器据此跳过写配额 */
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

/* uploadRateLimit 限制单用户每小时上传会话数量 */
func uploadRateLimit(limiter port.RateLimiter) gin.HandlerFunc {
	return rateLimit(limiter, func(c *gin.Context) string {
		claims, ok := accessClaimsFrom(c)
		if !ok || claims.UserID == "" {
			return ""
		}
		return "rl:upload:" + claims.UserID
	}, uploadRateAllowance, uploadRateWindow, "上传请求过于频繁")
}

/*
 * rateLimit 是限流中间件通用实现。
 * identity 返回空串表示无法确定主体（例如鉴权缺失），此时交给后续鉴权中间件处理，不消耗配额。
 */
func rateLimit(limiter port.RateLimiter, identity func(*gin.Context) string,
	allowance int, window time.Duration, message string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := identity(c)
		if key == "" {
			c.Next()
			return
		}
		allowed, wait, err := limiter.Take(c.Request.Context(), key, allowance, window)
		if err != nil {
			respondError(c, errs.New(errs.CodeInternal, "限流校验失败").WithCause(err))
			return
		}
		if !allowed {
			respondError(c, errs.New(errs.CodeRateLimited, message).
				WithMeta("retryAfterSeconds", retryAfterSeconds(wait)))
			return
		}
		c.Next()
	}
}

/* retryAfterSeconds 把剩余等待时间换算成至少 1 秒的建议重试间隔 */
func retryAfterSeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

/*
 * idempotent 保证创建类请求在 24 小时内同键同体只执行一次。
 * 先用 SETNX 占位取得执行权：并发同键请求只有一个进入处理器，另一个收到 409；
 * 失败响应会释放占位，客户端修正请求后可复用同一键重试。
 * 参考：docs/API.md#26-幂等
 */
func idempotent(store port.IdempotencyStore, now clock.Clock, logger *slog.Logger, scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, payload, err := prepareIdempotency(c)
		if err != nil {
			respondError(c, err)
			return
		}
		subject, hash := idempotencyScope(c, scope), requestHash(payload)
		if replaySnapshot(c, store, subject, key, hash) {
			return
		}

		placeholder := &port.IdempotencyRecord{RequestHash: hash, CreatedAt: now.Now()}
		acquired, err := store.Reserve(c.Request.Context(), subject, key, placeholder, idempotencyTTL)
		if err != nil {
			respondError(c, errs.New(errs.CodeInternal, "写入幂等占位失败").WithCause(err))
			return
		}
		if !acquired {
			/* 并发同键：另一请求可能已完成（回放）或仍在执行（409） */
			if replaySnapshot(c, store, subject, key, hash) {
				return
			}
			respondError(c, errs.New(errs.CodeIdempotencyInProgress, "相同请求正在处理，请稍后重试"))
			return
		}
		executeAndSnapshot(c, store, now, logger, subject, key, hash)
	}
}

/* prepareIdempotency 校验头部并读出请求体；读取后立即回填，处理器仍可正常解析 */
func prepareIdempotency(c *gin.Context) (string, []byte, error) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		return "", nil, errs.New(errs.CodeValidationFailed, "Idempotency-Key 必须为 8–64 位可见 ASCII")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", nil, errs.New(errs.CodeValidationFailed, "读取请求体失败").WithCause(err)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(payload))
	return key, payload, nil
}

/* idempotencyScope 在登录态下按用户隔离键空间，避免不同账号互相覆盖 */
func idempotencyScope(c *gin.Context, scope string) string {
	if claims, ok := accessClaimsFrom(c); ok && claims.UserID != "" {
		return scope + ":" + claims.UserID
	}
	return scope
}

/* requestHash 只保存请求体的摘要，避免把密码等明文落进幂等快照 */
func requestHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

/* replaySnapshot 命中且请求体一致时回放首次响应；占位记录返回 409，返回 true 表示链路已结束 */
func replaySnapshot(c *gin.Context, store port.IdempotencyStore, scope, key, hash string) bool {
	record, err := store.Get(c.Request.Context(), scope, key)
	if err != nil {
		respondError(c, errs.New(errs.CodeInternal, "读取幂等快照失败").WithCause(err))
		return true
	}
	if record == nil {
		return false
	}

	/* 同键不同体属于客户端错误，必须 409 而不是覆盖首次结果 */
	if record.RequestHash != hash {
		respondError(c, errs.New(errs.CodeIdempotencyReused, "同一 Idempotency-Key 携带了不同请求体"))
		return true
	}
	if record.Pending() {
		respondError(c, errs.New(errs.CodeIdempotencyInProgress, "相同请求正在处理，请稍后重试"))
		return true
	}
	c.Data(record.Status, record.ContentType, record.Body)
	c.Abort()
	return true
}

/*
 * executeAndSnapshot 用代理写入器捕获响应，仅在 2xx 时落库。
 * 失败响应不落库并释放占位，客户端修正请求后可复用同一键重试；panic 同样释放占位。
 */
func executeAndSnapshot(c *gin.Context, store port.IdempotencyStore, now clock.Clock, logger *slog.Logger, scope, key, hash string) {
	recorder := &snapshotWriter{ResponseWriter: c.Writer}
	c.Writer = recorder
	snapshotted := false
	defer func() {
		if snapshotted {
			return
		}
		releasePlaceholder(store, logger, scope, key, hash)
	}()

	c.Next()
	if !replayable(recorder.status) {
		return
	}

	/* 快照写入失败不影响已发出的响应，只记录告警以便排查存储异常 */
	record := buildRecord(recorder, hash, now)
	if err := store.Put(context.WithoutCancel(c.Request.Context()), scope, key, record, idempotencyTTL); err != nil {
		logger.Warn("写入幂等快照失败",
			"scope", scope,
			"requestId", requestid.From(c.Request.Context()),
			"error", err,
		)
		return
	}
	snapshotted = true
}

/* releasePlaceholder 释放执行权；失败仅记录告警，占位会随 TTL 自然过期 */
func releasePlaceholder(store port.IdempotencyStore, logger *slog.Logger, scope, key, hash string) {
	if err := store.Release(context.Background(), scope, key, hash); err != nil {
		logger.Warn("释放幂等占位失败", "scope", scope, "error", err)
	}
}

/* replayable 只缓存成功响应；4xx/5xx 允许客户端修正后用同一键重试 */
func replayable(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

/* buildRecord 复制响应体，避免与代理写入器的缓冲区共享底层数组 */
func buildRecord(recorder *snapshotWriter, hash string, now clock.Clock) *port.IdempotencyRecord {
	return &port.IdempotencyRecord{
		RequestHash: hash,
		Status:      recorder.status,
		ContentType: recorder.Header().Get("Content-Type"),
		Body:        append([]byte(nil), recorder.body.Bytes()...),
		CreatedAt:   now.Now(),
	}
}

/* validIdempotencyKey 要求 8–64 位可见 ASCII，防止换行注入日志与缓存键 */
func validIdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 64 {
		return false
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x21 || key[index] > 0x7E {
			return false
		}
	}
	return true
}

/* snapshotWriter 在透传写操作的同时缓存状态码与响应体 */
type snapshotWriter struct {
	gin.ResponseWriter
	status int
	body   bytes.Buffer
}

/* WriteHeader 记录首次状态码；重复调用由底层 ResponseWriter 忽略 */
func (w *snapshotWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

/* Write 在未显式写头时按 200 处理，并缓存响应体 */
func (w *snapshotWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.Write(payload)
	return w.ResponseWriter.Write(payload)
}

/* WriteString 复用 Write 以保持缓存与落盘一致 */
func (w *snapshotWriter) WriteString(payload string) (int, error) {
	return w.Write([]byte(payload))
}
