/*
 * Package problem 把业务错误码映射为 RFC 9457 的 problem+json 响应体。
 *
 * 职责：错误码 → HTTP 状态码、英文标题、中文用户话术、是否可重试的唯一映射点；
 * 负责填充 requestId、实例路径与模板变量。
 * 不负责：写 HTTP 响应（由 transport/http 调用本包后落地），不定义业务错误码。
 * 依赖：domain/errs、标准库。
 * 参考：docs/ARCHITECTURE.md#82-错误模型、RFC 9457 <https://www.rfc-editor.org/rfc/rfc9457.html>
 */
package problem

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* Body 是 RFC 9457 的成员与对齐 AIP-193 的扩展成员，字段名即契约 */
type Body struct {
	Type              string         `json:"type"`
	Title             string         `json:"title"`
	Status            int            `json:"status"`
	Detail            string         `json:"detail"`
	Instance          string         `json:"instance"`
	Code              errs.Code      `json:"code"`
	UserMessage       string         `json:"userMessage"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	Retryable         bool           `json:"retryable"`
	RetryAfterSeconds int            `json:"retryAfterSeconds,omitempty"`
	RequestID         string         `json:"requestId"`
}

/* spec 是错误码在传输层的外在表现，与业务细节解耦 */
type spec struct {
	status      int
	title       string
	userMessage string
	retryable   bool
}

/* catalog 是错误码目录；键集合必须与 errs 的常量一一对应 */
var catalog = map[errs.Code]spec{
	errs.CodeValidationFailed:    {400, "Validation failed", "输入内容有误，请检查后重试", false},
	errs.CodeResourceNotFound:    {404, "Resource not found", "内容不存在或已被删除", false},
	errs.CodeNameDuplicated:      {409, "Name duplicated", "当前目录下已存在同名目录", false},
	errs.CodeRevisionMismatch:    {412, "Revision mismatch", "内容已更新，请刷新后重试", false},
	errs.CodeFolderDepthExceeded: {409, "Folder depth exceeded", "最多支持 5 层目录", false},
	errs.CodeFolderCycle:         {409, "Folder cycle", "不能移动到自身或其子目录", false},
	errs.CodeNoteTooLarge:        {413, "Note too large", "正文已达上限，无法继续输入", false},
	errs.CodeNotePageLimit:       {409, "Page limit exceeded", "单篇笔记最多 50 张图片", false},
	errs.CodeNoteStateInvalid:    {409, "Note state invalid", "当前笔记状态不支持该操作", false},
	errs.CodeTagLimitExceeded:    {409, "Tag limit exceeded", "每篇笔记最多 10 个标签", false},
	errs.CodeQuotaExceeded:       {409, "Quota exceeded", "今日额度剩余 {remaining} 张，可减少图片后重试", false},
	errs.CodeStorageExceeded:     {409, "Storage exceeded", "云端空间不足，请清理后重试", false},
	errs.CodePreconditionFailed:  {409, "Precondition failed", "当前状态不支持该操作", false},
}

/* 认证与账号码的映射，文案与 docs/API.md#61-认证与账号 保持一致 */
var authCatalog = map[errs.Code]spec{
	errs.CodeAuthInvalidCredentials: {401, "Invalid credentials", "邮箱或密码不正确", false},
	errs.CodeAuthEmailNotRegistered: {404, "Email not registered", "该邮箱尚未注册", false},
	errs.CodeAuthEmailAlreadyExists: {409, "Email already exists", "该邮箱已注册，请直接登录", false},
	errs.CodeAuthCodeInvalid:        {400, "Invalid verification code", "验证码不正确，请重新输入", false},
	errs.CodeAuthCodeExpired:        {400, "Verification code expired", "验证码已过期，请重新获取", false},
	errs.CodeAuthCodeRateLimited:    {429, "Verification code rate limited", "操作过于频繁，请 {retryAfterSeconds} 秒后重试", true},
	errs.CodeAuthTooManyAttempts:    {429, "Too many attempts", "尝试次数过多，请 {retryAfterSeconds} 秒后重试", true},
	errs.CodeAuthTokenExpired:       {401, "Access token expired", "登录已过期，请重新登录", false},
	errs.CodeAuthTokenInvalid:       {401, "Invalid token", "登录状态异常，请重新登录", false},
	errs.CodeAuthRefreshReused:      {401, "Refresh token reused", "登录状态异常，请重新登录", false},
	errs.CodeAuthAgreementRequired:  {400, "Agreement required", "请先阅读并同意用户协议与隐私政策", false},
}

/* 上传与转换码的映射，文案与 docs/API.md#63-上传与转换 保持一致 */
var uploadCatalog = map[errs.Code]spec{
	errs.CodeUploadNotFound:         {404, "Upload not found", "图片上传记录不存在，请重新上传", false},
	errs.CodeUploadExpired:          {410, "Upload expired", "上传已超时，请重新上传", false},
	errs.CodeUploadNotReady:         {409, "Upload not ready", "图片尚未上传完成", false},
	errs.CodeUploadSizeMismatch:     {409, "Upload size mismatch", "图片上传不完整，请重试", false},
	errs.CodeUploadHashMismatch:     {409, "Upload hash mismatch", "图片校验失败，请重新上传", false},
	errs.CodeUploadUnsupportedType:  {415, "Unsupported media type", "仅支持 JPG、PNG、WEBP 格式", false},
	errs.CodeConversionNotFound:     {404, "Conversion job not found", "暂无转换任务", false},
	errs.CodeConversionStateInvalid: {409, "Conversion state invalid", "当前转换状态不支持该操作", false},
	errs.CodeConversionFailed:       {409, "Conversion failed", "转换失败，请重试", false},
	errs.CodeProviderUnavailable:    {503, "Provider unavailable", "当前使用人数较多，请稍后重试", true},
	errs.CodeImageUnreadable:        {422, "Image unreadable", "部分页面不清晰，建议重拍后重试", false},
	errs.CodeContentUnsupported:     {422, "Content unsupported", "该内容暂不支持转换", false},
	errs.CodeConversionTimeout:      {504, "Conversion timeout", "转换超时，请重试", true},
	errs.CodeEmptyResult:            {422, "Empty result", "未能识别到可用的文字内容", false},
}

/* 分享、同步与通用码的映射，文案与 docs/API.md#64-分享同步与通用 保持一致 */
var generalCatalog = map[errs.Code]spec{
	errs.CodeShareLinkInactive: {410, "Share link inactive", "内容已失效", false},
	errs.CodeShareNoteNotReady: {409, "Share note not ready", "请先完成转换再分享", false},
	errs.CodeShareLinkNotFound: {404, "Share link not found", "该笔记尚未开启分享", false},
	errs.CodeSyncCursorExpired: {410, "Sync cursor expired", "数据已更新，请重新同步", false},
	errs.CodeInvalidPageToken:  {400, "Invalid page token", "列表已变化，请刷新后重试", false},
	errs.CodeIdempotencyReused: {409, "Idempotency key reused", "操作冲突，请刷新后重试", false},
	errs.CodeIdempotencyInProgress: {409, "Idempotency request in progress",
		"相同请求正在处理，请稍后重试", false},
	errs.CodeRateLimited: {429, "Rate limited", "操作过于频繁，请稍后重试", true},
	errs.CodeInternal:    {500, "Internal error", "服务异常，请稍后重试", true},
	errs.CodeUnavailable: {503, "Service unavailable", "服务暂时不可用，请稍后重试", true},
	errs.CodeTimeout:     {504, "Request timeout", "请求超时，请重试", true},
}

/* lookup 返回错误码对应的传输层表现；未知码统一按 INTERNAL 处理 */
func lookup(code errs.Code) spec {
	if found, ok := catalog[code]; ok {
		return found
	}
	if found, ok := authCatalog[code]; ok {
		return found
	}
	if found, ok := uploadCatalog[code]; ok {
		return found
	}
	if found, ok := generalCatalog[code]; ok {
		return found
	}
	return spec{status: 500, title: "Internal error", userMessage: "服务异常，请稍后重试", retryable: true}
}

/*
 * From 把任意错误转换为状态码与响应体。
 * 未携带业务错误码的错误一律按 INTERNAL 输出，Detail 使用固定文案，避免泄露内部信息。
 */
func From(err error, instance, requestID string) (int, Body) {
	business, ok := errs.As(err)
	if !ok {
		code, detail := classifyFrameworkError(err)
		return build(errs.New(code, detail), instance, requestID)
	}
	return build(business, instance, requestID)
}

/* classifyFrameworkError 把上下文取消与超时翻译为稳定错误码 */
func classifyFrameworkError(err error) (errs.Code, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return errs.CodeTimeout, "上游依赖或请求处理超时"
	}
	if errors.Is(err, context.Canceled) {
		return errs.CodeInternal, "请求已被取消"
	}
	return errs.CodeInternal, "未预期的内部错误"
}

/* build 组装响应体；元数据与模板变量都来自业务错误本身 */
func build(business *errs.Error, instance, requestID string) (int, Body) {
	spec := lookup(business.Code())
	metadata := business.Meta()
	body := Body{
		Type:        typeURI(business.Code()),
		Title:       spec.title,
		Status:      spec.status,
		Detail:      detailOrTitle(business.Detail(), spec.title),
		Instance:    withRequestID(instance, requestID),
		Code:        business.Code(),
		UserMessage: fillTemplate(spec.userMessage, metadata),
		Metadata:    metadata,
		Retryable:   spec.retryable,
		RequestID:   requestID,
	}
	if seconds, ok := retryAfter(metadata); ok {
		body.RetryAfterSeconds = seconds
	}
	return spec.status, body
}

/* typeURI 生成稳定的问题类型 URI，规则由错误码小写连字符化得到 */
func typeURI(code errs.Code) string {
	return "https://gleannote.example/problems/" + strings.ReplaceAll(strings.ToLower(string(code)), "_", "-")
}

/* detailOrTitle 保证 Detail 必填，同时不把空串透出给客户端 */
func detailOrTitle(detail, title string) string {
	if strings.TrimSpace(detail) == "" {
		return title
	}
	return detail
}

/* withRequestID 按契约把请求 ID 拼到实例路径后，空 ID 时保持原样 */
func withRequestID(instance, requestID string) string {
	if requestID == "" {
		return instance
	}
	return instance + "#" + requestID
}

/* fillTemplate 用元数据填充 {key} 占位；缺失的变量保留原样便于发现漏填 */
func fillTemplate(template string, metadata map[string]any) string {
	result := template
	for key, value := range metadata {
		result = strings.ReplaceAll(result, "{"+key+"}", fmt.Sprint(value))
	}
	return result
}

/* retryAfter 从元数据读取建议重试间隔，兼容 JSON 反序列化得到的多种数值类型 */
func retryAfter(metadata map[string]any) (int, bool) {
	raw, exists := metadata["retryAfterSeconds"]
	if !exists {
		return 0, false
	}
	seconds, ok := toInt(raw)
	if !ok || seconds <= 0 {
		return 0, false
	}
	return seconds, true
}

/* toInt 覆盖 int、int64、float64 与字符串四种来源类型 */
func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
