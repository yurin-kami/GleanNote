/* 已标记 EXPIRED 的会话不再签发地址，避免过期凭据被重复使用 */
package upload

import (
	"context"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestPresignUploadRejectsExpiredState 校验 EXPIRED 状态直接返回 UPLOAD_EXPIRED */
func TestPresignUploadRejectsExpiredState(t *testing.T) {
	f := newFixture(t)
	uploadID := f.createUpload(t, "user-1", "image/jpeg", []byte("fake-jpeg-bytes"))

	stored, err := f.uploads.GetByID(context.Background(), "user-1", uploadID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	stored.MarkExpired()
	if err := f.uploads.Update(context.Background(), stored); err != nil {
		t.Fatalf("更新会话失败: %v", err)
	}

	if _, err := f.service.PresignUpload(context.Background(), "user-1", uploadID); !errs.IsCode(err, errs.CodeUploadExpired) {
		t.Fatalf("EXPIRED 会话续签错误 = %v，期望 UPLOAD_EXPIRED", err)
	}
}
