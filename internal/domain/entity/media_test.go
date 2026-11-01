/* 领域层新增值对象与实体的单元测试：图片校验、上传/页面/转换任务状态机 */
package entity_test

import (
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* requireCode 断言错误链中的业务错误码 */
func requireCode(t *testing.T, err error, want errs.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际成功", want)
	}
	if code, _ := errs.CodeOf(err); code != want {
		t.Fatalf("错误码 = %s，期望 %s（%v）", code, want, err)
	}
}

/* TestParseImageContentType 校验格式白名单与大小写归一 */
func TestParseImageContentType(t *testing.T) {
	for _, raw := range []string{"image/jpeg", "image/png", " image/webp "} {
		if _, err := valueobject.ParseImageContentType(raw); err != nil {
			t.Fatalf("合法类型 %q 被拒绝: %v", raw, err)
		}
	}
	if _, err := valueobject.ParseImageContentType("image/gif"); err == nil {
		t.Fatal("未知类型应被拒绝")
	}
	if got := valueobject.ImagePNG.Extension(); got != ".png" {
		t.Fatalf("扩展名 = %q，期望 .png", got)
	}
}

/* TestParseSHA256 校验摘要长度、字符集与空白裁剪 */
func TestParseSHA256(t *testing.T) {
	sha := ""
	for index := 0; index < 64; index++ {
		sha += "a"
	}
	parsed, err := valueobject.ParseSHA256(" " + sha + " ")
	if err != nil || parsed != sha {
		t.Fatalf("合法摘要被拒绝: %v", err)
	}
	requireCode(t, mustErr(valueobject.ParseSHA256("abc")), errs.CodeValidationFailed)
	requireCode(t, mustErr(valueobject.ParseSHA256("A"+sha[1:])), errs.CodeValidationFailed)
}

/* mustErr 只保留错误，便于在断言中内联调用 */
func mustErr(_ string, err error) error { return err }

/* TestImageLimits 校验字节上限与像素上限 */
func TestImageLimits(t *testing.T) {
	if err := valueobject.ValidateImageBytes(valueobject.MaxImageBytes); err != nil {
		t.Fatalf("10MB 应合法: %v", err)
	}
	requireCode(t, valueobject.ValidateImageBytes(valueobject.MaxImageBytes+1), errs.CodeValidationFailed)
	requireCode(t, valueobject.ValidateImageBytes(0), errs.CodeValidationFailed)

	if !valueobject.PixelCountWithinLimit(4000, 4000) {
		t.Fatal("1600 万像素应在上限内")
	}
	if valueobject.PixelCountWithinLimit(8000, 8000) {
		t.Fatal("6400 万像素应超限")
	}
}

/* TestUploadLifecycle 校验上传会话的过期与完成标记 */
func TestUploadLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	upload := entity.NewUpload("u1", "user", "u/user/2026/10/u1.jpg", "p1.jpg",
		"image/jpeg", "abc", 1024, now.Add(30*time.Minute), now)
	if upload.Expired(now.Add(29 * time.Minute)) {
		t.Fatal("30 分钟内不应过期")
	}
	if !upload.Expired(now.Add(30 * time.Minute)) {
		t.Fatal("到点应视为过期")
	}

	upload.MarkReady(1600, 1200, "thumb/u1.jpg", "abc")
	if !upload.IsReady() {
		t.Fatal("MarkReady 后应为 READY")
	}
	if upload.Expired(now.Add(24 * time.Hour)) {
		t.Fatal("READY 会话不因 30 分钟窗口失效")
	}
}

/* TestNotePageInheritsUpload 校验页面继承原图与缩略图键 */
func TestNotePageInheritsUpload(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	upload := entity.NewUpload("u1", "user", "u/user/u1.png", "p1.png",
		"image/png", "hash", 2048, now.Add(time.Hour), now)
	upload.MarkReady(800, 600, "thumb/u1.jpg", "hash")

	page := entity.NewNotePage("p1", "n1", "user", 1, upload, now)
	if page.ObjectKey != upload.ObjectKey || page.ThumbKey != upload.ThumbKey {
		t.Fatalf("页面未继承对象键: %+v", page)
	}
	if page.Bytes != 2048 || page.Width != 800 || page.Index != 1 {
		t.Fatalf("页面元数据不一致: %+v", page)
	}
}

/* TestConversionJobTransitions 校验任务状态机、失败文案与取消约束 */
func TestConversionJobTransitions(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	job := entity.NewConversionJob("j1", "n1", "user", entity.JobKindCreate, 3, 3, 1, now)
	if job.State != entity.JobStateQueued || job.Phase != entity.JobPhaseQueued {
		t.Fatalf("初始状态错误: %+v", job)
	}

	job.Start(now.Add(time.Second))
	if job.State != entity.JobStateRunning || job.StartedAt == nil {
		t.Fatalf("Start 未生效: %+v", job)
	}
	job.Advance(2, entity.JobPhaseGenerating, now.Add(2*time.Second))
	if job.ProcessedPages != 2 {
		t.Fatalf("进度 = %d，期望 2", job.ProcessedPages)
	}
	job.Succeed(now.Add(3 * time.Second))
	if !job.IsTerminal() || job.Phase != entity.JobPhaseCompleted || job.ProcessedPages != 3 {
		t.Fatalf("成功状态错误: %+v", job)
	}
	requireCode(t, job.Cancel(now.Add(4*time.Second)), errs.CodeConversionStateInvalid)

	failed := entity.NewConversionJob("j2", "n1", "user", entity.JobKindCreate, 1, 1, 2, now)
	failed.Start(now)
	failed.Fail(entity.JobFailureTimeout, now.Add(time.Minute))
	if failed.FailureCode != entity.JobFailureTimeout ||
		failed.FailureUserMessage != "转换超时，请重试" {
		t.Fatalf("失败码或文案错误: %+v", failed)
	}

	cancelable := entity.NewConversionJob("j3", "n1", "user", entity.JobKindCreate, 1, 1, 1, now)
	if err := cancelable.Cancel(now); err != nil || cancelable.State != entity.JobStateCanceled {
		t.Fatalf("取消排队任务失败: %v", err)
	}
	if cancelable.CanCancel() {
		t.Fatal("终态任务不应可取消")
	}
}
