/*
 * 转换结果中的「未识别页面」归一。
 *
 * 视觉适配器已经把页码结构化返回（port.VisionResult.UnreadablePages）；
 * 正文里的占位文案只作为兜底，用于未实现该字段的第三方适配器与历史数据。
 * 参考：docs/API.md#551-任务对象、docs/PRD-拾页笔记-GleanNote.md FR-05
 */
package conversion

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* unreadablePlaceholder 与 vision 适配器输出的占位完全一致，括号为全角 */
var unreadablePlaceholder = regexp.MustCompile("（第\\s*(\\d+)\\s*页未识别到文字）")

/*
 * resolveUnreadablePages 优先采用适配器给出的结构化页码，缺失时回退到正文占位解析。
 * 之所以按「长度」而不是「非 nil」判断：适配器可能返回空切片，语义与 nil 相同。
 */
func resolveUnreadablePages(result *port.VisionResult) []int {
	if len(result.UnreadablePages) > 0 {
		return normalizePageNumbers(result.UnreadablePages)
	}
	return collectUnreadablePages(result.Markdown)
}

/*
 * collectUnreadablePages 从正文占位中提取页码，非法值忽略。
 * 页码来自模型输出，解析失败不应让整篇转换失败：正文本身仍然可用。
 */
func collectUnreadablePages(markdown string) []int {
	matches := unreadablePlaceholder.FindAllStringSubmatch(markdown, -1)
	pages := make([]int, 0, len(matches))
	for _, match := range matches {
		page, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		pages = append(pages, page)
	}
	return normalizePageNumbers(pages)
}

/*
 * normalizePageNumbers 丢弃非正页码、去重并升序。
 * 约束：同一份结果多次归一必须得到同一顺序，客户端可直接按下标渲染。
 */
func normalizePageNumbers(pages []int) []int {
	seen := make(map[int]struct{}, len(pages))
	for _, page := range pages {
		if page <= 0 {
			continue
		}
		seen[page] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	unique := make([]int, 0, len(seen))
	for page := range seen {
		unique = append(unique, page)
	}
	sort.Ints(unique)
	return unique
}
