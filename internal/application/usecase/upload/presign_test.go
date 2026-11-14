/* 上传会话重新签发的用例行为：续传、越权、过期与已完成态 */
package upload

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestPresignUploadReissuesTicket 校验续签返回同一会话的新地址与必带请求头 */
func TestPresignUploadReissuesTicket(t *testing.T) {
	f := newFixture(t)
	uploadID := f.createUpload(t, "user-1", "image/jpeg", []byte("fake-jpeg-bytes"))

	ticket, err := f.service.PresignUpload(context.Background(), "user-1", uploadID)
	if err != nil {
		t.Fatalf("重新签发失败: %v", err)
	}
	if ticket.UploadID != uploadID {
		t.Fatalf("uploadId = %q，期望 %q", ticket.UploadID, uploadID)
	}
	if !strings.HasPrefix(ticket.PutURL, "memory://") {
		t.Fatalf("putUrl = %q，期望 fake 存储的 memory:// 地址", ticket.PutURL)
	}
	if got := ticket.Headers["Content-Type"]; got != "image/jpeg" {
		t.Fatalf("headers.Content-Type = %q，期望 image/jpeg", got)
	}

	/* 地址有效期与创建时的会话到期时间一致，不会因续签被延长 */
	stored, err := f.uploads.GetByID(context.Background(), "user-1", uploadID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if !ticket.ExpiresAt.Equal(stored.ExpiresAt) {
		t.Fatalf("expiresAt = %v，期望 %v", ticket.ExpiresAt, stored.ExpiresAt)
	}
}

/* TestPresignUploadRejectsOtherUser 校验跨账号续签按资源不存在处理，避免暴露 uploadId */
func TestPresignUploadRejectsOtherUser(t *testing.T) {
	f := newFixture(t)
	uploadID := f.createUpload(t, "user-1", "image/png", []byte("fake-png-bytes"))

	if _, err := f.service.PresignUpload(context.Background(), "user-2", uploadID); !errs.IsCode(err, errs.CodeUploadNotFound) {
		t.Fatalf("跨账号续签错误 = %v，期望 UPLOAD_NOT_FOUND", err)
	}
}

/* TestPresignUploadExpiredSession 校验超过 30 分钟窗口后续签返回 UPLOAD_EXPIRED 并落状态 */
func TestPresignUploadExpiredSession(t *testing.T) {
	f := newFixture(t)
	uploadID := f.createUpload(t, "user-1", "image/jpeg", []byte("fake-jpeg-bytes"))

	f.clock.Advance(UploadSessionTTL + time.Minute)
	if _, err := f.service.PresignUpload(context.Background(), "user-1", uploadID); !errs.IsCode(err, errs.CodeUploadExpired) {
		t.Fatalf("过期续签错误 = %v，期望 UPLOAD_EXPIRED", err)
	}
	stored, err := f.uploads.GetByID(context.Background(), "user-1", uploadID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if stored.Status != "EXPIRED" {
		t.Fatalf("过期会话状态 = %q，期望 EXPIRED", stored.Status)
	}
}

/* TestPresignUploadRejectsCompletedSession 校验已完成的会话不再签发地址 */
func TestPresignUploadRejectsCompletedSession(t *testing.T) {
	f := newFixture(t)
	uploadID := f.createUpload(t, "user-1", "image/jpeg", []byte("fake-jpeg-bytes"))

	stored, err := f.uploads.GetByID(context.Background(), "user-1", uploadID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	stored.MarkReady(10, 10, "thumb/"+uploadID+".jpg", stored.SHA256)
	if err := f.uploads.Update(context.Background(), stored); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}

	if _, err := f.service.PresignUpload(context.Background(), "user-1", uploadID); !errs.IsCode(err, errs.CodePreconditionFailed) {
		t.Fatalf("已完成会话续签错误 = %v，期望 PRECONDITION_FAILED", err)
	}
}
