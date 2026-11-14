/* 未识别页归一：结构化优先、正文兜底、去重排序，以及任务落库与视图透出 */
package conversion

import (
	"context"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* TestResolvePrefersAdapterPages 校验适配器给出结构化页码时不再解析正文 */
func TestResolvePrefersAdapterPages(t *testing.T) {
	result := &port.VisionResult{
		Markdown:        "## 第 1 页\n\n正文",
		UnreadablePages: []int{3, 1, 3},
	}
	pages := resolveUnreadablePages(result)
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 3 {
		t.Fatalf("未识别页 = %v，期望 [1 3]", pages)
	}
}

/* TestResolveFallsBackToMarkdown 校验适配器未给页码时回退到正文占位 */
func TestResolveFallsBackToMarkdown(t *testing.T) {
	result := &port.VisionResult{
		Markdown: "## 第 1 页\n\n正文\n\n（第 2 页未识别到文字）",
	}
	pages := resolveUnreadablePages(result)
	if len(pages) != 1 || pages[0] != 2 {
		t.Fatalf("未识别页 = %v，期望 [2]", pages)
	}
}

/* TestNormalizePageNumbersDropsInvalid 校验非正页码被丢弃、重复页码被合并 */
func TestNormalizePageNumbersDropsInvalid(t *testing.T) {
	pages := normalizePageNumbers([]int{0, -1, 2, 2, 1})
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Fatalf("归一结果 = %v，期望 [1 2]", pages)
	}
	if got := normalizePageNumbers([]int{0, -3}); got != nil {
		t.Fatalf("全非法输入 = %v，期望 nil", got)
	}
}

/* TestCollectUnreadablePagesIgnoresNoise 校验没有占位时返回 nil，正文不被误判 */
func TestCollectUnreadablePagesIgnoresNoise(t *testing.T) {
	if pages := collectUnreadablePages("## 第 1 页\n\n正文（第 页）"); pages != nil {
		t.Fatalf("未识别页 = %v，期望 nil", pages)
	}
}

/* TestRunJobRecordsUnreadablePages 校验任务成功时把结构化页码落库并透出到视图 */
func TestRunJobRecordsUnreadablePages(t *testing.T) {
	f := newFixture(t)
	f.seedDraft(t, "note-1", "user-a", 2)
	f.seedJob(t, "job-1", "note-1", "user-a", 2, entity.JobStateQueued)
	/* 正文不含占位，页码只来自适配器结构化字段，确保优先路径被覆盖 */
	f.vision.result = &port.VisionResult{
		Markdown:        "## 第 1 页\n\n正文\n\n## 第 2 页\n\n空白页",
		UnreadablePages: []int{2},
		Provider:        "stub",
	}

	if err := f.service.RunJob(context.Background(), "user-a", "job-1"); err != nil {
		t.Fatalf("执行任务失败: %v", err)
	}
	job, err := f.jobs.GetByID(context.Background(), "user-a", "job-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if job.State != entity.JobStateSucceeded {
		t.Fatalf("任务状态 = %q，期望 SUCCEEDED", job.State)
	}
	if len(job.UnreadablePages) != 1 || job.UnreadablePages[0] != 2 {
		t.Fatalf("任务未识别页 = %v，期望 [2]", job.UnreadablePages)
	}

	view := toJobView(job)
	if len(view.UnreadablePages) != 1 || view.UnreadablePages[0] != 2 {
		t.Fatalf("任务视图未识别页 = %v，期望 [2]", view.UnreadablePages)
	}
}
