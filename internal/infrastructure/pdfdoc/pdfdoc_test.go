package pdfdoc

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* pageCountPattern 提取页面树中的页数，用于校验分页结果 */
var pageCountPattern = regexp.MustCompile(`/Count (\d+)`)

/* jpegImage 构造一张可嵌入的最小 JPEG 记录；渲染器不解析像素内容，只读取元数据 */
func jpegImage(width, height int) port.PDFImage {
	return port.PDFImage{
		Data:        []byte{0xFF, 0xD8, 0xFF, 0xD9},
		Width:       width,
		Height:      height,
		ContentType: "image/jpeg",
	}
}

/* documentPages 解析渲染结果中的页数，解析失败直接终止用例 */
func documentPages(t *testing.T, raw []byte) int {
	t.Helper()
	matched := pageCountPattern.FindSubmatch(raw)
	if matched == nil {
		t.Fatal("PDF 缺少页面树 /Count")
	}
	count, err := strconv.Atoi(string(matched[1]))
	if err != nil {
		t.Fatalf("页面树 /Count 不是整数: %v", err)
	}
	return count
}

/* TestRenderProducesValidPDFStructure 验证文件头、字体、交叉引用与文件尾齐备且至少一页 */
func TestRenderProducesValidPDFStructure(t *testing.T) {
	raw, err := New().Render(context.Background(), port.PDFDocument{
		Title:   "拾页笔记",
		Content: "# 一级标题\n\n正文内容",
	})
	if err != nil {
		t.Fatalf("渲染 PDF: %v", err)
	}
	text := string(raw)
	for _, marker := range []string{"%PDF-1.4", "/Type /Catalog", "/BaseFont /STSong-Light", "xref", "startxref", "%%EOF"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("PDF 缺少结构标记 %q", marker)
		}
	}
	if pages := documentPages(t, raw); pages != 1 {
		t.Fatalf("页数 = %d，短文档应为 1 页", pages)
	}
}

/* TestRenderFallsBackToUnnamedTitle 验证空标题回落「未命名笔记」，不生成空文本块 */
func TestRenderFallsBackToUnnamedTitle(t *testing.T) {
	raw, err := New().Render(context.Background(), port.PDFDocument{Content: "正文"})
	if err != nil {
		t.Fatalf("渲染 PDF: %v", err)
	}
	if !strings.Contains(string(raw), utf16Hex("未命名笔记")) {
		t.Fatal("空标题未回落为「未命名笔记」")
	}
}

/*
 * TestRenderEmbedsOnlyJPEG 验证只有 JPEG 进入 XObject。
 * 其他格式若被强行嵌入会生成客户端无法解码的页面，因此必须跳过。
 */
func TestRenderEmbedsOnlyJPEG(t *testing.T) {
	raw, err := New().Render(context.Background(), port.PDFDocument{
		Title: "图片文档",
		Images: []port.PDFImage{
			jpegImage(640, 480),
			{Data: []byte("png"), Width: 100, Height: 100, ContentType: "image/png"},
		},
	})
	if err != nil {
		t.Fatalf("渲染 PDF: %v", err)
	}
	text := string(raw)
	if count := strings.Count(text, "/Filter /DCTDecode"); count != 1 {
		t.Fatalf("嵌入图片数 = %d，期望仅 1 张 JPEG", count)
	}
	if !strings.Contains(text, "/Im0") {
		t.Fatal("页面资源字典缺少 JPEG 引用")
	}
}

/* TestRenderPaginatesLongContent 验证长正文按页面高度切分，图片不跨页截断 */
func TestRenderPaginatesLongContent(t *testing.T) {
	var builder bytes.Buffer
	for index := 0; index < 120; index++ {
		builder.WriteString("第 ")
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString(" 段正文内容，用于触发分页。\n")
	}
	document := port.PDFDocument{Title: "长文档", Content: builder.String(), Images: []port.PDFImage{jpegImage(800, 600)}}
	raw, err := New().Render(context.Background(), document)
	if err != nil {
		t.Fatalf("渲染 PDF: %v", err)
	}
	if pages := documentPages(t, raw); pages < 2 {
		t.Fatalf("页数 = %d，长文档应至少 2 页", pages)
	}
	/* 图片必须整体落在某一页：绘制指令 /Im0 Do 只出现一次，不会随分页被复制 */
	if count := strings.Count(string(raw), "/Im0 Do"); count != 1 {
		t.Fatalf("/Im0 Do 次数 = %d，期望 1", count)
	}
}

/* TestRenderHonorsContext 验证渲染前先响应取消，避免导出任务在已中止请求上继续算 */
func TestRenderHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Render(ctx, port.PDFDocument{Title: "取消"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Render 错误 = %v，期望 context.Canceled", err)
	}
}

/* TestWrapLineKeepsShortText 验证折行不改变短文本，且全角字符按一字宽估算 */
func TestWrapLineKeepsShortText(t *testing.T) {
	lines := wrapLine("短句", bodySize)
	if len(lines) != 1 || lines[0] != "短句" {
		t.Fatalf("wrapLine = %q，期望单行", lines)
	}
	if wrapped := wrapLine("", bodySize); wrapped != nil {
		t.Fatalf("空文本 wrapLine = %q，期望 nil", wrapped)
	}
}
