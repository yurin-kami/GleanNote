/* 测试错误码到 RFC 9457 响应的映射：状态码、模板变量、元数据与兜底分支 */
package problem

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 每个已定义错误码都必须有映射，防止新增码漏配导致状态码漂移 */
func TestCatalogCoversEveryCode(t *testing.T) {
	codes := []errs.Code{
		errs.CodeValidationFailed, errs.CodeResourceNotFound, errs.CodeNameDuplicated,
		errs.CodeRevisionMismatch, errs.CodeFolderDepthExceeded, errs.CodeFolderCycle,
		errs.CodeNoteTooLarge, errs.CodeNotePageLimit, errs.CodeNoteStateInvalid,
		errs.CodeTagLimitExceeded, errs.CodeQuotaExceeded, errs.CodeStorageExceeded,
		errs.CodePreconditionFailed, errs.CodeAuthInvalidCredentials,
	}
	codes = append(codes,
		errs.CodeAuthEmailNotRegistered, errs.CodeAuthEmailAlreadyExists,
		errs.CodeAuthCodeInvalid, errs.CodeAuthCodeExpired, errs.CodeAuthCodeRateLimited,
		errs.CodeAuthTooManyAttempts, errs.CodeAuthTokenExpired, errs.CodeAuthTokenInvalid,
		errs.CodeAuthRefreshReused, errs.CodeAuthAgreementRequired,
	)
	codes = append(codes,
		errs.CodeUploadNotFound, errs.CodeUploadExpired, errs.CodeUploadNotReady,
		errs.CodeUploadSizeMismatch, errs.CodeUploadHashMismatch, errs.CodeUploadUnsupportedType,
		errs.CodeConversionNotFound, errs.CodeConversionStateInvalid, errs.CodeConversionFailed,
		errs.CodeProviderUnavailable, errs.CodeImageUnreadable, errs.CodeContentUnsupported,
		errs.CodeConversionTimeout, errs.CodeEmptyResult,
	)
	codes = append(codes,
		errs.CodeShareLinkInactive, errs.CodeShareNoteNotReady, errs.CodeShareLinkNotFound,
		errs.CodeSyncCursorExpired, errs.CodeInvalidPageToken, errs.CodeIdempotencyReused,
		errs.CodeRateLimited, errs.CodeInternal, errs.CodeUnavailable, errs.CodeTimeout,
	)
	for _, code := range codes {
		if !covered(code) {
			t.Fatalf("错误码 %s 缺少传输层映射", code)
		}
	}
}

/* covered 检查码是否存在于任一目录分组 */
func covered(code errs.Code) bool {
	if _, ok := catalog[code]; ok {
		return true
	}
	if _, ok := authCatalog[code]; ok {
		return true
	}
	if _, ok := uploadCatalog[code]; ok {
		return true
	}
	_, ok := generalCatalog[code]
	return ok
}

/* 映射必须填充全部必选成员，并保持错误码与状态码一致 */
func TestFromBuildsCompleteBody(t *testing.T) {
	status, body := From(errs.New(errs.CodeAuthCodeRateLimited, "60 秒内重复发送"), "/v1/auth:sendCode", "req_0123456789ab")
	if status != 429 {
		t.Fatalf("期望 429，实际 %d", status)
	}
	if body.Code != errs.CodeAuthCodeRateLimited || body.Status != 429 {
		t.Fatalf("错误码或状态不一致: %s %d", body.Code, body.Status)
	}
	if body.Type != "https://gleannote.example/problems/auth-code-rate-limited" {
		t.Fatalf("类型 URI 不符: %s", body.Type)
	}
	if body.Instance != "/v1/auth:sendCode#req_0123456789ab" {
		t.Fatalf("实例路径不符: %s", body.Instance)
	}
	if body.RequestID != "req_0123456789ab" {
		t.Fatalf("请求 ID 不符: %s", body.RequestID)
	}
	if body.Detail != "60 秒内重复发送" {
		t.Fatalf("细节不符: %s", body.Detail)
	}
}

/* 模板变量与重试间隔必须来自元数据，而不是细节文本解析 */
func TestMetaTemplateAndRetryAfter(t *testing.T) {
	business := errs.New(errs.CodeAuthTooManyAttempts, "账号已锁定").
		WithMeta("retryAfterSeconds", 42).
		WithMeta("lockedUntil", "2026-10-28T06:15:00Z")
	_, body := From(business, "/v1/auth:login", "req_x")

	if !strings.Contains(body.UserMessage, "42") {
		t.Fatalf("模板未填充: %s", body.UserMessage)
	}
	if body.RetryAfterSeconds != 42 {
		t.Fatalf("重试间隔不符: %d", body.RetryAfterSeconds)
	}
	if !body.Retryable {
		t.Fatal("429 应标记可重试")
	}
	if body.Metadata["lockedUntil"] != "2026-10-28T06:15:00Z" {
		t.Fatal("元数据应原样透出")
	}
}

/* 非业务错误、未知业务码与超时都必须落到稳定兜底，且不泄露内部细节 */
func TestFallbackBranches(t *testing.T) {
	status, body := From(errors.New("sql: password=secret"), "/v1/notes", "req_1")
	if status != 500 || body.Code != errs.CodeInternal || !body.Retryable {
		t.Fatalf("未知错误兜底不符: %d %s", status, body.Code)
	}
	if strings.Contains(body.Detail, "secret") {
		t.Fatal("兜底细节泄露了内部信息")
	}

	status, body = From(context.DeadlineExceeded, "/v1/sync:changes", "req_2")
	if status != 504 || body.Code != errs.CodeTimeout || !body.Retryable {
		t.Fatalf("超时映射不符: %d %s", status, body.Code)
	}

	status, body = From(errs.New(errs.CodeValidationFailed, ""), "/v1/folders", "")
	if status != 400 || body.Detail != "Validation failed" {
		t.Fatalf("空细节应回落到标题: %s", body.Detail)
	}
	if body.Instance != "/v1/folders" {
		t.Fatalf("空请求 ID 不应追加分隔符: %s", body.Instance)
	}
}
