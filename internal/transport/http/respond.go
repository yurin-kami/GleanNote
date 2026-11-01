/*
 * 传输层响应与上下文辅助：JSON 输出、问题渲染、鉴权上下文与原始路径。
 *
 * 职责：处理器只通过本文件写响应，保证状态码、Content-Type 与请求 ID 口径一致。
 * 不负责：错误码到 HTTP 的映射（见 platform/problem）。
 * 参考：docs/API.md#28-错误模型、docs/API.md#23-请求与响应头
 */
package httptransport

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/problem"
	"github.com/gleannote/gleannote/internal/platform/requestid"
)

/* 传输层固定响应头；问题响应必须使用 problem+json，客户端据此切换解析器 */
const (
	jsonContentType     = "application/json; charset=utf-8"
	problemContentType  = "application/problem+json; charset=utf-8"
	maxRequestBodyBytes = 12 << 20
)

/* 上下文键使用字符串常量，gin.Context 的 Set/Get 只接受字符串键 */
const claimsContextKey = "gleannote.accessClaims"

/* originalPathKey 以私有类型存放原始路径，避免与其他包的上下文键冲突 */
type originalPathKey struct{}

/* respondJSON 序列化成功后一次性写出；序列化失败说明响应结构有缺陷，按内部错误兜底 */
func respondJSON(c *gin.Context, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		respondError(c, errs.New(errs.CodeInternal, "响应序列化失败").WithCause(err))
		return
	}
	c.Data(status, jsonContentType, encoded)
}

/* respondNoContent 用于 204 端点；不写响应体，避免客户端误解析空 JSON */
func respondNoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

/*
 * respondError 把任意错误渲染为 RFC 9457 响应。
 * 约束：响应已写出时只中止链路，避免二次写头导致连接被截断。
 * 参考：docs/API.md#28-错误模型
 */
func respondError(c *gin.Context, err error) {
	if c.Writer.Written() {
		c.Abort()
		return
	}
	requestID := requestid.From(c.Request.Context())
	status, body := problem.From(err, instancePath(c), requestID)
	if body.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(body.RetryAfterSeconds))
	}

	/* 体结构固定，序列化失败属于实现缺陷；兜底仍返回问题响应以维持契约 */
	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": errs.CodeInternal})
		return
	}
	c.Data(status, problemContentType, encoded)
	c.Abort()
}

/* instancePath 优先使用改写前的原始路径，保证 problem.instance 与契约一致 */
func instancePath(c *gin.Context) string {
	path := c.Request.URL.Path
	if original, ok := c.Request.Context().Value(originalPathKey{}).(string); ok && original != "" {
		path = original
	}
	return redactPath(path)
}

/* redactPath 把公开分享路径中的令牌替换为占位符，避免令牌进入日志与问题响应（G-10） */
func redactPath(path string) string {
	const prefix = "/v1/public/shares/"
	if index := strings.Index(path, prefix); index >= 0 {
		return path[:index+len(prefix)] + "{token}"
	}
	return path
}

/* setAccessClaims 把鉴权结果放入 gin 上下文，供处理器与访问日志读取 */
func setAccessClaims(c *gin.Context, claims port.AccessClaims) {
	c.Set(claimsContextKey, claims)
}

/* accessClaimsFrom 读取鉴权结果；未经过鉴权中间件时返回 false */
func accessClaimsFrom(c *gin.Context) (port.AccessClaims, bool) {
	value, exists := c.Get(claimsContextKey)
	if !exists {
		return port.AccessClaims{}, false
	}
	claims, ok := value.(port.AccessClaims)
	return claims, ok
}

/* currentUserID 读取鉴权中间件写入的用户 ID，缺失说明路由漏挂中间件 */
func currentUserID(c *gin.Context) (string, bool) {
	claims, ok := accessClaimsFrom(c)
	if !ok || claims.UserID == "" {
		return "", false
	}
	return claims.UserID, true
}

/* isJSONContentType 只接受 application/json，忽略大小写与 charset 参数 */
func isJSONContentType(raw string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	return mediaType == "application/json"
}

/*
 * bindJSON 统一请求体解析口径：限制 12MB、拒绝非 JSON 类型并将解码错误归一为 VALIDATION_FAILED。
 * 约束：调用方必须在写任何响应之前调用，本函数不写响应。
 */
func bindJSON(c *gin.Context, dst any) error {
	if !isJSONContentType(c.GetHeader("Content-Type")) {
		return errs.New(errs.CodeValidationFailed, "Content-Type 必须为 application/json")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(dst); err != nil {
		return decodeError(err)
	}
	return nil
}

/* decodeError 区分超出上限、空请求体与格式错误，便于客户端定位问题 */
func decodeError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errs.New(errs.CodeValidationFailed, "请求体超过 12MB 上限")
	}
	if errors.Is(err, io.EOF) {
		return errs.New(errs.CodeValidationFailed, "请求体不能为空")
	}
	return errs.New(errs.CodeValidationFailed, "请求体不是合法的 JSON").WithCause(err)
}
