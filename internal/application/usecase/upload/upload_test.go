/* 测试上传会话申请、完成校验、幂等与页面读取 */
package upload

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* codeOf 提取业务错误码，便于断言 */
func codeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	return code
}

/* jpegBytes 生成指定尺寸的 JPEG 字节，用于完成上传校验 */
func jpegBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	source.Set(0, 0, color.RGBA{R: 200, G: 120, B: 60, A: 255})
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, source, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("编码 JPEG 失败: %v", err)
	}
	return buffer.Bytes()
}

/* TestCreateUploadsValidation 校验批量数量、类型、字节与摘要的边界 */
func TestCreateUploadsValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	valid := []byte("payload")
	base := UploadItem{FileName: "p.jpg", ContentType: "image/jpeg", Bytes: int64(len(valid)), SHA256: digest(valid)}

	if _, err := f.service.CreateUploads(ctx, "user-a", nil); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("空批次应返回 VALIDATION_FAILED，实际 %v", err)
	}
	many := make([]UploadItem, 21)
	for index := range many {
		many[index] = base
	}
	if _, err := f.service.CreateUploads(ctx, "user-a", many); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("超过 20 项应返回 VALIDATION_FAILED，实际 %v", err)
	}

	broken := base
	broken.ContentType = "image/gif"
	if _, err := f.service.CreateUploads(ctx, "user-a", []UploadItem{broken}); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("不支持的类型应返回 VALIDATION_FAILED，实际 %v", err)
	}
	broken = base
	broken.Bytes = 0
	if _, err := f.service.CreateUploads(ctx, "user-a", []UploadItem{broken}); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("字节为 0 应返回 VALIDATION_FAILED，实际 %v", err)
	}
	broken = base
	broken.SHA256 = strings.Repeat("z", 64)
	if _, err := f.service.CreateUploads(ctx, "user-a", []UploadItem{broken}); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("非法摘要应返回 VALIDATION_FAILED，实际 %v", err)
	}
}

/* TestCreateUploadsTicket 校验会话写入、对象键与预签名有效期 */
func TestCreateUploadsTicket(t *testing.T) {
	f := newFixture(t)
	data := jpegBytes(t, 32, 32)
	tickets, err := f.service.CreateUploads(context.Background(), "user-a", []UploadItem{{
		FileName:    "p.jpg",
		ContentType: "image/jpeg",
		Bytes:       int64(len(data)),
		SHA256:      digest(data),
	}})
	if err != nil {
		t.Fatalf("申请上传失败: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("凭据数量 = %d，期望 1", len(tickets))
	}
	ticket := tickets[0]
	if !strings.HasPrefix(ticket.ObjectKey, "u/") || !strings.HasSuffix(ticket.ObjectKey, ".jpg") {
		t.Fatalf("对象键 = %q，不符合 u/{前缀}/日期/{id}.jpg", ticket.ObjectKey)
	}
	if ticket.PutURL == "" || ticket.Headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("预签名地址或请求头缺失: %+v", ticket)
	}
	expected := f.clock.Now().Add(UploadSessionTTL)
	if !ticket.ExpiresAt.Equal(expected) {
		t.Fatalf("过期时间 = %s，期望 %s", ticket.ExpiresAt, expected)
	}

	stored, err := f.uploads.GetByID(context.Background(), "user-a", ticket.UploadID)
	if err != nil || stored.Status != entity.UploadStatusPending {
		t.Fatalf("会话状态 = %+v，err=%v", stored, err)
	}
}

/* TestCompleteUploadReadyAndIdempotent 校验字节、摘要、解码与缩略图写入，并验证幂等 */
func TestCompleteUploadReadyAndIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data := jpegBytes(t, 640, 480)
	uploadID := f.createUpload(t, "user-a", "image/jpeg", data)

	stored, err := f.uploads.GetByID(ctx, "user-a", uploadID)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if err := f.objects.Put(ctx, stored.ObjectKey, "image/jpeg", data); err != nil {
		t.Fatalf("写入对象失败: %v", err)
	}

	result, err := f.service.CompleteUpload(ctx, "user-a", uploadID, digest(data))
	if err != nil {
		t.Fatalf("完成上传失败: %v", err)
	}
	if result.Status != string(entity.UploadStatusReady) || result.Width != 640 || result.Height != 480 {
		t.Fatalf("完成结果 = %+v", result)
	}
	if result.ThumbURL == "" {
		t.Fatal("缩略图地址为空")
	}
	ready, _ := f.uploads.GetByID(ctx, "user-a", uploadID)
	if !ready.IsReady() || ready.ThumbKey != "thumb/"+uploadID+".jpg" {
		t.Fatalf("会话状态 = %+v", ready)
	}
	if _, err := f.objects.Stat(ctx, ready.ThumbKey); err != nil {
		t.Fatalf("缩略图对象缺失: %v", err)
	}

	again, err := f.service.CompleteUpload(ctx, "user-a", uploadID, digest(data))
	if err != nil || again.Status != result.Status {
		t.Fatalf("重复完成应幂等: %+v err=%v", again, err)
	}
}

/* TestCompleteUploadRejectsMismatch 校验大小、摘要、类型与过期分支 */
func TestCompleteUploadRejectsMismatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data := jpegBytes(t, 40, 40)
	uploadID := f.createUpload(t, "user-a", "image/jpeg", data)
	stored, _ := f.uploads.GetByID(ctx, "user-a", uploadID)

	/* 对象缺失：视为上传不完整 */
	if _, err := f.service.CompleteUpload(ctx, "user-a", uploadID, digest(data)); codeOf(err) != errs.CodeUploadSizeMismatch {
		t.Fatalf("对象缺失应返回 UPLOAD_SIZE_MISMATCH，实际 %v", err)
	}
	if err := f.objects.Put(ctx, stored.ObjectKey, "image/jpeg", data); err != nil {
		t.Fatalf("写入对象失败: %v", err)
	}
	if _, err := f.service.CompleteUpload(ctx, "user-a", uploadID, digest([]byte("other"))); codeOf(err) != errs.CodeUploadHashMismatch {
		t.Fatalf("摘要不符应返回 UPLOAD_HASH_MISMATCH，实际 %v", err)
	}

	f.clock.Advance(UploadSessionTTL + time.Minute)
	if _, err := f.service.CompleteUpload(ctx, "user-a", uploadID, digest(data)); codeOf(err) != errs.CodeUploadExpired {
		t.Fatalf("超时应返回 UPLOAD_EXPIRED，实际 %v", err)
	}
	expired, _ := f.uploads.GetByID(ctx, "user-a", uploadID)
	if expired.Status != entity.UploadStatusExpired {
		t.Fatalf("会话状态 = %q，期望 EXPIRED", expired.Status)
	}
}

/* TestListPagesAndGetPage 校验页面分页、缩略图地址与原图地址 */
func TestListPagesAndGetPage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedNote(t, "note-1", "user-a")

	pages := make([]*entity.NotePage, 0, 3)
	for index := 1; index <= 3; index++ {
		upload := entity.NewUpload("up-"+string(rune('0'+index)), "user-a", "u/up.jpg", "p.jpg",
			"image/jpeg", strings.Repeat("a", 64), 1024, f.clock.Now().Add(time.Hour), f.clock.Now())
		upload.MarkReady(800, 600, "thumb/up-"+string(rune('0'+index))+".jpg", "")
		pages = append(pages, entity.NewNotePage("page-"+string(rune('0'+index)), "note-1", "user-a",
			index, upload, f.clock.Now()))
	}
	if err := f.pages.CreateBatch(ctx, pages); err != nil {
		t.Fatalf("写入页面失败: %v", err)
	}

	first, err := f.service.ListPages(ctx, "user-a", "note-1", 2, "")
	if err != nil {
		t.Fatalf("首页失败: %v", err)
	}
	if len(first.Items) != 2 || first.NextPageToken == "" {
		t.Fatalf("首页 = %+v，期望 2 条且有下一页令牌", first)
	}
	if first.Items[0].Index != 1 || first.Items[0].ThumbURL == "" {
		t.Fatalf("首页首项 = %+v", first.Items[0])
	}
	second, err := f.service.ListPages(ctx, "user-a", "note-1", 2, first.NextPageToken)
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Index != 3 || second.NextPageToken != "" {
		t.Fatalf("第二页 = %+v", second)
	}

	detail, err := f.service.GetPage(ctx, "user-a", "note-1", "page-1")
	if err != nil {
		t.Fatalf("读取原图地址失败: %v", err)
	}
	if !strings.HasPrefix(detail.OriginalURL, "memory://") || detail.ExpiresAt.IsZero() {
		t.Fatalf("原图地址 = %+v", detail)
	}

	if _, err := f.service.ListPages(ctx, "user-b", "note-1", 2, ""); codeOf(err) != errs.CodeResourceNotFound {
		t.Fatalf("跨用户读取应返回 RESOURCE_NOT_FOUND，实际 %v", err)
	}
}
