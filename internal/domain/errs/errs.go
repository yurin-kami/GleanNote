/*
 * Package errs 定义全局稳定的业务错误码与领域错误类型。
 *
 * 职责：领域与应用层只产出「错误码 + 中文/英文细节 + 结构化元数据」，
 * 由 platform/problem 统一映射为 RFC 9457 响应，避免领域依赖 HTTP。
 * 不负责：HTTP 状态码与用户话术（见 platform/problem 的目录表）。
 * 依赖：仅标准库。
 * 参考：docs/API.md#6-错误码目录、docs/ARCHITECTURE.md#82-错误模型
 */
package errs

import (
	"errors"
	"fmt"
)

/* Code 是全局唯一、与 HTTP 状态码绑定的稳定错误码 */
type Code string

/*
 * 错误码目录。
 * 新增只能追加，禁止重命名或复用既有值（AIP-180 / docs/API.md#7-兼容与版本策略）。
 */
const (
	CodeValidationFailed    Code = "VALIDATION_FAILED"
	CodeResourceNotFound    Code = "RESOURCE_NOT_FOUND"
	CodeNameDuplicated      Code = "NAME_DUPLICATED"
	CodeRevisionMismatch    Code = "REVISION_MISMATCH"
	CodeFolderDepthExceeded Code = "FOLDER_DEPTH_EXCEEDED"
	CodeFolderCycle         Code = "FOLDER_CYCLE"
	CodeNoteTooLarge        Code = "NOTE_TOO_LARGE"
	CodeNotePageLimit       Code = "NOTE_PAGE_LIMIT_EXCEEDED"
	CodeNoteStateInvalid    Code = "NOTE_STATE_INVALID"
	CodeTagLimitExceeded    Code = "TAG_LIMIT_EXCEEDED"
	CodeQuotaExceeded       Code = "QUOTA_EXCEEDED"
	CodeStorageExceeded     Code = "STORAGE_EXCEEDED"
	CodePreconditionFailed  Code = "PRECONDITION_FAILED"
)

/* 认证与账号类错误码，语义见 docs/API.md#61-认证与账号 */
const (
	CodeAuthInvalidCredentials Code = "AUTH_INVALID_CREDENTIALS"
	CodeAuthEmailNotRegistered Code = "AUTH_EMAIL_NOT_REGISTERED"
	CodeAuthEmailAlreadyExists Code = "AUTH_EMAIL_ALREADY_EXISTS"
	CodeAuthCodeInvalid        Code = "AUTH_CODE_INVALID"
	CodeAuthCodeExpired        Code = "AUTH_CODE_EXPIRED"
	CodeAuthCodeRateLimited    Code = "AUTH_CODE_RATE_LIMITED"
	CodeAuthTooManyAttempts    Code = "AUTH_TOO_MANY_ATTEMPTS"
	CodeAuthTokenExpired       Code = "AUTH_TOKEN_EXPIRED"
	CodeAuthTokenInvalid       Code = "AUTH_TOKEN_INVALID"
	CodeAuthRefreshReused      Code = "AUTH_REFRESH_REUSED"
	CodeAuthAgreementRequired  Code = "AUTH_AGREEMENT_REQUIRED"
)

/* 上传与转换类错误码，语义见 docs/API.md#63-上传与转换 */
const (
	CodeUploadNotFound         Code = "UPLOAD_NOT_FOUND"
	CodeUploadExpired          Code = "UPLOAD_EXPIRED"
	CodeUploadNotReady         Code = "UPLOAD_NOT_READY"
	CodeUploadSizeMismatch     Code = "UPLOAD_SIZE_MISMATCH"
	CodeUploadHashMismatch     Code = "UPLOAD_HASH_MISMATCH"
	CodeUploadUnsupportedType  Code = "UPLOAD_UNSUPPORTED_TYPE"
	CodeConversionNotFound     Code = "CONVERSION_NOT_FOUND"
	CodeConversionStateInvalid Code = "CONVERSION_STATE_INVALID"
	CodeConversionFailed       Code = "CONVERSION_FAILED"
	CodeProviderUnavailable    Code = "PROVIDER_UNAVAILABLE"
	CodeImageUnreadable        Code = "IMAGE_UNREADABLE"
	CodeContentUnsupported     Code = "CONTENT_UNSUPPORTED"
	CodeConversionTimeout      Code = "CONVERSION_TIMEOUT"
	CodeEmptyResult            Code = "EMPTY_RESULT"
)

/* 分享、同步与通用错误码，语义见 docs/API.md#64-分享同步与通用 */
const (
	CodeShareLinkInactive     Code = "SHARE_LINK_INACTIVE"
	CodeShareNoteNotReady     Code = "SHARE_NOTE_NOT_READY"
	CodeShareLinkNotFound     Code = "SHARE_LINK_NOT_FOUND"
	CodeSyncCursorExpired     Code = "SYNC_CURSOR_EXPIRED"
	CodeInvalidPageToken      Code = "INVALID_PAGE_TOKEN"
	CodeIdempotencyReused     Code = "IDEMPOTENCY_KEY_REUSED"
	CodeIdempotencyInProgress Code = "IDEMPOTENCY_IN_PROGRESS"
	CodeRateLimited           Code = "RATE_LIMITED"
	CodeInternal              Code = "INTERNAL"
	CodeUnavailable           Code = "UNAVAILABLE"
	CodeTimeout               Code = "TIMEOUT"
)

/*
 * Error 是领域与应用层共用的错误类型。
 * Detail 面向开发排查，Meta 承载客户端可读取的变量，Cause 保留底层错误链且不得下发。
 */
type Error struct {
	code   Code
	detail string
	meta   map[string]any
	cause  error
}

/* New 构造一个不附带元数据的业务错误 */
func New(code Code, detail string) *Error {
	return &Error{code: code, detail: detail}
}

/* Error 实现 error 接口；有底层原因时附带，便于日志定位 */
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.code, e.detail, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.code, e.detail)
}

/* Unwrap 暴露底层原因，支持 errors.Is/As 链式判断 */
func (e *Error) Unwrap() error {
	return e.cause
}

/* Code 返回稳定的业务错误码 */
func (e *Error) Code() Code {
	return e.code
}

/* Detail 返回面向开发的英文细节 */
func (e *Error) Detail() string {
	return e.detail
}

/* Meta 返回结构化元数据的只读副本，避免调用方修改共享状态 */
func (e *Error) Meta() map[string]any {
	if len(e.meta) == 0 {
		return nil
	}
	copied := make(map[string]any, len(e.meta))
	for key, value := range e.meta {
		copied[key] = value
	}
	return copied
}

/* WithMeta 追加或覆盖一个元数据键，返回同一实例以便链式构造 */
func (e *Error) WithMeta(key string, value any) *Error {
	if e.meta == nil {
		e.meta = make(map[string]any, 2)
	}
	e.meta[key] = value
	return e
}

/* WithCause 挂接底层原因；该原因只进入日志，不会出现在 HTTP 响应 */
func (e *Error) WithCause(cause error) *Error {
	e.cause = cause
	return e
}

/* As 提取错误链中的业务错误 */
func As(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

/* CodeOf 返回错误链中的业务错误码；非业务错误返回 false */
func CodeOf(err error) (Code, bool) {
	if target, ok := As(err); ok {
		return target.Code(), true
	}
	return "", false
}

/* IsCode 判断错误链中是否包含指定业务错误码 */
func IsCode(err error, code Code) bool {
	actual, ok := CodeOf(err)
	return ok && actual == code
}

/* IsValidation 是校验类失败的快捷判断，传输层用于统一 400 分支 */
func IsValidation(err error) bool {
	return IsCode(err, CodeValidationFailed)
}
