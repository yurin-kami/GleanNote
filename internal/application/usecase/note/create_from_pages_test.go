/* 由图片创建笔记、页面回填与彻底删除释放容量的用例测试 */
package note

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/platform/bizdate"
)

/* remaining 读取当日剩余额度 */
func (f *fixture) remaining(t *testing.T, userID string) int {
	t.Helper()
	value, err := f.ledger.Remaining(context.Background(), userID, bizdate.Day(f.clock.Now()))
	if err != nil {
		t.Fatalf("读取额度: %v", err)
	}
	return value
}

/* submit 调用 createFromPages 并要求成功，返回结果 */
func (f *fixture) submit(t *testing.T, userID, folderID string, uploadIDs []string, title string) CreateFromPagesResult {
	t.Helper()
	result, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    userID,
		FolderID:  folderID,
		UploadIDs: uploadIDs,
		Title:     title,
	})
	if err != nil {
		t.Fatalf("创建转换笔记失败: %v", err)
	}
	return result
}

/* TestCreateFromPagesSuccess 校验草稿、页面顺序、任务入队与额度容量扣减 */
func TestCreateFromPagesSuccess(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	f.seedUpload(t, "up-1", "user-a", 2048, true)
	f.seedUpload(t, "up-2", "user-a", 3072, true)

	result := f.submit(t, "user-a", root.ID, []string{"up-2", "up-1"}, "")

	if result.Note.State != string(entity.NoteStateDraft) || result.Note.Revision != 1 {
		t.Fatalf("笔记 = %+v，期望 DRAFT/revision=1", result.Note)
	}
	if result.Note.PageCount != 2 || result.Note.ThumbnailURL == "" {
		t.Fatalf("页面回填 = count:%d thumb:%q", result.Note.PageCount, result.Note.ThumbnailURL)
	}
	if result.Job.State != string(entity.JobStateQueued) || result.Job.TotalPages != 2 {
		t.Fatalf("任务 = %+v", result.Job)
	}
	if result.Quota.Charged != 2 || result.Quota.Remaining != valueobject.DailyPageQuota-2 {
		t.Fatalf("额度 = %+v", result.Quota)
	}

	pages, err := f.pages.ListAll(context.Background(), "user-a", result.Note.ID)
	if err != nil || len(pages) != 2 {
		t.Fatalf("页面 = %d，err=%v", len(pages), err)
	}
	/* uploadIds 顺序即页面顺序：第一页应指向 up-2 */
	if pages[0].Index != 1 || pages[0].ObjectKey != "u/up-2.jpg" {
		t.Fatalf("首屏 = %+v", pages[0])
	}

	used, err := f.usage.Used(context.Background(), "user-a")
	if err != nil || used != 5120 {
		t.Fatalf("容量 = %d，err=%v，期望 5120", used, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	userID, jobID, ok, err := f.queue.Next(ctx)
	if err != nil || !ok || userID != "user-a" || jobID != result.Job.ID {
		t.Fatalf("队列 = %q ok=%v err=%v", jobID, ok, err)
	}
}

/* TestCreateFromPagesQuotaExceeded 校验额度不足时不写入任何数据 */
func TestCreateFromPagesQuotaExceeded(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	if err := f.ledger.Charge(context.Background(), "user-a", "seed", bizdate.Day(f.clock.Now()),
		valueobject.DailyPageQuota, valueobject.DailyPageQuota); err != nil {
		t.Fatalf("预占额度: %v", err)
	}
	f.seedUpload(t, "up-1", "user-a", 1024, true)

	_, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    "user-a",
		FolderID:  root.ID,
		UploadIDs: []string{"up-1"},
	})
	requireCode(t, err, errs.CodeQuotaExceeded)

	if _, err := f.notes.GetByID(context.Background(), "user-a", "id-1001"); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("额度不足不应创建笔记，err=%v", err)
	}
}

/* TestCreateFromPagesStorageExceeded 校验容量不足时返回 STORAGE_EXCEEDED */
func TestCreateFromPagesStorageExceeded(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	if err := f.usage.Reserve(context.Background(), "user-a",
		valueobject.StorageLimitBytes-1024, valueobject.StorageLimitBytes); err != nil {
		t.Fatalf("预占容量: %v", err)
	}
	f.seedUpload(t, "up-1", "user-a", 4096, true)

	_, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    "user-a",
		FolderID:  root.ID,
		UploadIDs: []string{"up-1"},
	})
	requireCode(t, err, errs.CodeStorageExceeded)
}

/* TestCreateFromPagesUploadNotReady 校验未完成校验的上传不可引用 */
func TestCreateFromPagesUploadNotReady(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	f.seedUpload(t, "up-1", "user-a", 1024, false)

	_, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    "user-a",
		FolderID:  root.ID,
		UploadIDs: []string{"up-1"},
	})
	requireCode(t, err, errs.CodeUploadNotReady)
}

/* TestCreateFromPagesExpiredUpload 校验过期会话返回 UPLOAD_EXPIRED 并落 EXPIRED */
func TestCreateFromPagesExpiredUpload(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	/* 已 READY 的会话不受 30 分钟窗口约束，因此用未完成校验的会话验证过期分支 */
	upload := f.seedUpload(t, "up-1", "user-a", 1024, false)
	f.clock.Advance(2 * time.Hour)

	_, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    "user-a",
		FolderID:  root.ID,
		UploadIDs: []string{"up-1"},
	})
	requireCode(t, err, errs.CodeUploadExpired)

	stored, err := f.uploads.GetByID(context.Background(), "user-a", upload.ID)
	if err != nil || stored.Status != entity.UploadStatusExpired {
		t.Fatalf("状态 = %q，err=%v，期望 EXPIRED", stored.Status, err)
	}
}

/* TestCreateFromPagesValidation 校验数量、重复与越权上传 */
func TestCreateFromPagesValidation(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	for index := 0; index < valueobject.MaxUploadBatch+1; index++ {
		f.seedUpload(t, "up-"+string(rune('a'+index)), "user-a", 512, true)
	}

	tooMany := make([]string, 0, valueobject.MaxUploadBatch+1)
	for index := 0; index < valueobject.MaxUploadBatch+1; index++ {
		tooMany = append(tooMany, "up-"+string(rune('a'+index)))
	}
	if _, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID: "user-a", FolderID: root.ID, UploadIDs: tooMany,
	}); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("超过 20 张应返回 VALIDATION_FAILED，实际 %v", err)
	}

	if _, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID: "user-a", FolderID: root.ID, UploadIDs: []string{"up-a", "up-a"},
	}); codeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("重复上传应返回 VALIDATION_FAILED，实际 %v", err)
	}

	f.seedUpload(t, "up-other", "user-b", 512, true)
	if _, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID: "user-a", FolderID: root.ID, UploadIDs: []string{"up-other"},
	}); codeOf(err) != errs.CodeUploadNotFound {
		t.Fatalf("他人上传应返回 UPLOAD_NOT_FOUND，实际 %v", err)
	}
}

/* TestExpungeNoteReleasesPages 校验彻底删除清空页面任务并释放容量与对象 */
func TestExpungeNoteReleasesPages(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	f.seedUpload(t, "up-1", "user-a", 4096, true)
	result := f.submit(t, "user-a", root.ID, []string{"up-1"}, "草稿")

	f.delete(t, "user-a", result.Note.ID)
	if err := f.service.ExpungeNote(context.Background(), "user-a", result.Note.ID); err != nil {
		t.Fatalf("彻底删除失败: %v", err)
	}

	if _, err := f.notes.GetByID(context.Background(), "user-a", result.Note.ID); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("笔记应已删除，err=%v", err)
	}
	if count, err := f.pages.CountByNote(context.Background(), "user-a", result.Note.ID); err != nil || count != 0 {
		t.Fatalf("页面残留 %d，err=%v", count, err)
	}
	if _, err := f.jobs.GetLatestByNote(context.Background(), "user-a", result.Note.ID); !errs.IsCode(err, errs.CodeConversionNotFound) {
		t.Fatalf("任务应已删除，err=%v", err)
	}
	if used, err := f.usage.Used(context.Background(), "user-a"); err != nil || used != 0 {
		t.Fatalf("容量 = %d，err=%v，期望释放为 0", used, err)
	}
}

/* TestListNotesFillsPageSummary 校验列表批量回填页面数与缩略图地址 */
func TestListNotesFillsPageSummary(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-a")
	f.seedUpload(t, "up-1", "user-a", 1024, true)
	f.seedUpload(t, "up-2", "user-a", 1024, true)
	f.submit(t, "user-a", root.ID, []string{"up-1", "up-2"}, "两页")

	f.create(t, "user-a", root.ID, "空白", "")

	list, err := f.service.ListNotes(context.Background(), ListNotesInput{
		UserID: "user-a", FolderID: root.ID,
	})
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("条数 = %d", len(list.Items))
	}
	for _, item := range list.Items {
		if item.Title == "两页" {
			if item.PageCount != 2 || item.ThumbnailURL == "" {
				t.Fatalf("图片笔记回填 = count:%d thumb:%q", item.PageCount, item.ThumbnailURL)
			}
		} else if item.PageCount != 0 || item.ThumbnailURL != "" {
			t.Fatalf("空白笔记不应有页面: %+v", item)
		}
	}
}

/* codeOf 提取业务错误码，便于断言 */
func codeOf(err error) errs.Code {
	code, _ := errs.CodeOf(err)
	return code
}
