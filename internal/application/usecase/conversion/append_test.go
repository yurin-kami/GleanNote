/* 追加转换测试：只处理新增页面并把结果拼接到最新正文末尾 */
package conversion

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* seedNamedNote 写入一篇已有正文的 NORMAL 笔记 */
func (f *fixture) seedNamedNote(t *testing.T, noteID, userID, title, content string) {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	parsedContent, err := valueobject.ParseNoteContent(content)
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	note := entity.NewNote(noteID, userID, "folder-1", parsedTitle, parsedContent, f.clock.Now(), f.clock.Now())
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
}

/* appendPage 向笔记追加一页，序号由调用方给出 */
func (f *fixture) appendPage(t *testing.T, noteID, userID string, index int) {
	t.Helper()
	suffix := strconv.Itoa(index)
	upload := entity.NewUpload("append-up-"+noteID+"-"+suffix, userID, "u/append.jpg", "p.jpg", "image/jpeg",
		strings.Repeat("a", 64), 2048, f.clock.Now().Add(-1), f.clock.Now())
	upload.MarkReady(800, 600, "thumb/append.jpg", strings.Repeat("a", 64))
	page := entity.NewNotePage("append-page-"+noteID+"-"+suffix, noteID, userID, index, upload, f.clock.Now())
	if err := f.pages.CreateBatch(context.Background(), []*entity.NotePage{page}); err != nil {
		t.Fatalf("写入页面: %v", err)
	}
}

/* TestRunAppendJobMergesContent 校验追加任务只处理新增页面并保留原标题与旧正文 */
func TestRunAppendJobMergesContent(t *testing.T) {
	f := newFixture(t)
	userID := "user-append"
	f.seedNamedNote(t, "note-append", userID, "矩阵的秩", "# 矩阵的秩\n\n原有内容")
	f.appendPage(t, "note-append", userID, 1)
	f.appendPage(t, "note-append", userID, 2)

	now := f.clock.Now()
	job := entity.NewConversionJob("job-append", "note-append", userID,
		entity.JobKindAppend, 1, 1, 1, now)
	if err := f.jobs.Create(context.Background(), job); err != nil {
		t.Fatalf("写入任务: %v", err)
	}
	f.vision.result = &port.VisionResult{Markdown: "## 追加页\n\n新增内容"}

	if err := f.service.RunJob(context.Background(), userID, job.ID); err != nil {
		t.Fatalf("执行任务: %v", err)
	}

	note, err := f.notes.GetByID(context.Background(), userID, "note-append")
	if err != nil {
		t.Fatalf("读取笔记: %v", err)
	}
	if note.Title != "矩阵的秩" {
		t.Fatalf("标题 = %q，追加不应改变标题", note.Title)
	}
	want := "# 矩阵的秩\n\n原有内容\n\n## 追加页\n\n新增内容"
	if note.Content != want {
		t.Fatalf("正文 = %q，期望 %q", note.Content, want)
	}
	if note.Revision != 2 {
		t.Fatalf("修订号 = %d，期望 2", note.Revision)
	}
	if len(f.versions.sources) != 1 || f.versions.sources[0] != "CONVERSION" {
		t.Fatalf("版本来源 = %v，期望 CONVERSION", f.versions.sources)
	}
	stored, err := f.jobs.GetByID(context.Background(), userID, job.ID)
	if err != nil || stored.State != entity.JobStateSucceeded {
		t.Fatalf("任务状态 = %+v（%v），期望 SUCCEEDED", stored, err)
	}
}
