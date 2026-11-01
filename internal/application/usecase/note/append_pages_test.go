/* 追加页面用例测试：状态、页面上限、额度与容量校验，以及任务提交 */
package note

import (
	"context"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* seedPages 直接写入指定数量的页面，用于构造接近上限的笔记 */
func (f *fixture) seedPages(t *testing.T, userID, noteID string, count int) {
	t.Helper()
	pages := make([]*entity.NotePage, 0, count)
	for index := 1; index <= count; index++ {
		upload := f.seedUpload(t, "page-upload-"+noteID+"-"+string(rune('a'+index)), userID, 1024, true)
		pages = append(pages, entity.NewNotePage("page-"+noteID+"-"+string(rune('a'+index)),
			noteID, userID, index, upload, f.clock.Now()))
	}
	if err := f.pages.CreateBatch(context.Background(), pages); err != nil {
		t.Fatalf("写入页面: %v", err)
	}
}

/* TestAppendPagesSubmitsAppendJob 校验正常笔记追加成功后提交 APPEND 任务并扣减额度 */
func TestAppendPagesSubmitsAppendJob(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-append")
	note := f.create(t, "user-append", root.ID, "线性代数", "# 线性代数")

	first := f.seedUpload(t, "append-u1", "user-append", 2048, true)
	second := f.seedUpload(t, "append-u2", "user-append", 4096, true)

	result, err := f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-append",
		NoteID:    note.ID,
		UploadIDs: []string{first.ID, second.ID},
	})
	if err != nil {
		t.Fatalf("追加页面失败: %v", err)
	}
	if result.Job.State != string(entity.JobStateQueued) || result.Job.TotalPages != 2 {
		t.Fatalf("任务不符: %+v", result.Job)
	}
	if result.Quota.Charged != 2 || result.Quota.Remaining != 48 {
		t.Fatalf("额度不符: %+v", result.Quota)
	}

	pages, err := f.pages.ListAll(context.Background(), "user-append", note.ID)
	if err != nil || len(pages) != 2 {
		t.Fatalf("页面数 = %d（%v），期望 2", len(pages), err)
	}
	if pages[0].Index != 1 || pages[1].Index != 2 {
		t.Fatalf("页面顺序不符: %+v", pages)
	}

	job, err := f.jobs.GetLatestByNote(context.Background(), "user-append", note.ID)
	if err != nil || job.Kind != entity.JobKindAppend {
		t.Fatalf("最近任务 = %+v（%v），期望 APPEND", job, err)
	}
	used, err := f.usage.Used(context.Background(), "user-append")
	if err != nil || used != 6144 {
		t.Fatalf("容量 = %d（%v），期望 6144", used, err)
	}
}

/* TestAppendPagesRejectsDraftAndDeleted 校验仅 NORMAL 笔记可追加 */
func TestAppendPagesRejectsDraftAndDeleted(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-state")
	upload := f.seedUpload(t, "state-u1", "user-state", 1024, true)

	draftView, err := f.service.CreateFromPages(context.Background(), CreateFromPagesInput{
		UserID:    "user-state",
		FolderID:  root.ID,
		UploadIDs: []string{upload.ID},
	})
	if err != nil {
		t.Fatalf("创建草稿: %v", err)
	}
	extra := f.seedUpload(t, "state-u2", "user-state", 1024, true)
	_, err = f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-state",
		NoteID:    draftView.Note.ID,
		UploadIDs: []string{extra.ID},
	})
	requireCode(t, err, errs.CodeNoteStateInvalid)

	normal := f.create(t, "user-state", root.ID, "正常笔记", "正文")
	f.delete(t, "user-state", normal.ID)
	_, err = f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-state",
		NoteID:    normal.ID,
		UploadIDs: []string{extra.ID},
	})
	requireCode(t, err, errs.CodeNoteStateInvalid)
}

/* TestAppendPagesEnforcesPageLimit 校验累计 50 页上限 */
func TestAppendPagesEnforcesPageLimit(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-limit")
	note := f.create(t, "user-limit", root.ID, "上限", "正文")
	f.seedPages(t, "user-limit", note.ID, valueobject.MaxNotePages-1)

	first := f.seedUpload(t, "limit-u1", "user-limit", 1024, true)
	second := f.seedUpload(t, "limit-u2", "user-limit", 1024, true)
	_, err := f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-limit",
		NoteID:    note.ID,
		UploadIDs: []string{first.ID, second.ID},
	})
	requireCode(t, err, errs.CodeNotePageLimit)

	ok := f.seedUpload(t, "limit-u3", "user-limit", 1024, true)
	if _, err := f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-limit",
		NoteID:    note.ID,
		UploadIDs: []string{ok.ID},
	}); err != nil {
		t.Fatalf("追加最后一页失败: %v", err)
	}
}

/* TestAppendPagesValidatesUploadsAndQuota 校验上传未就绪与额度不足 */
func TestAppendPagesValidatesUploadsAndQuota(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-check")
	note := f.create(t, "user-check", root.ID, "校验", "正文")

	pending := f.seedUpload(t, "check-pending", "user-check", 1024, false)
	_, err := f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-check",
		NoteID:    note.ID,
		UploadIDs: []string{pending.ID},
	})
	requireCode(t, err, errs.CodeUploadNotReady)

	ready := f.seedUpload(t, "check-ready", "user-check", 1024, true)
	if err := f.ledger.Charge(context.Background(), "user-check", "pre-charge", "2026-10-28",
		valueobject.DailyPageQuota, valueobject.DailyPageQuota); err != nil {
		t.Fatalf("预扣额度: %v", err)
	}
	_, err = f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-check",
		NoteID:    note.ID,
		UploadIDs: []string{ready.ID},
	})
	requireCode(t, err, errs.CodeQuotaExceeded)

	_, err = f.service.AppendPages(context.Background(), AppendPagesInput{
		UserID:    "user-check",
		NoteID:    "missing-note",
		UploadIDs: []string{ready.ID},
	})
	requireCode(t, err, errs.CodeResourceNotFound)
}
