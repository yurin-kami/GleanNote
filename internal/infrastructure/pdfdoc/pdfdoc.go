/*
 * Package pdfdoc 是导出 PDF 的最小渲染器：A4 版式、中文 Type0 字体与 JPEG 页面图片。
 *
 * 职责：把标题、Markdown 正文与页面图片排版为单文件 PDF 字节。
 * 不负责：对象存储读写、导出任务状态机与下载地址签发（由用例负责）。
 * 依赖：application/port（渲染入参）、标准库。
 * 参考：docs/PRD-拾页笔记-GleanNote.md#fr-14-导出-markdown-与-pdf、docs/ARCHITECTURE.md#56-页面图片管线
 */
package pdfdoc

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* A4 页面几何：单位 pt，边距 56pt 与常见打印排版一致 */
const (
	pageWidth    = 595.28
	pageHeight   = 841.89
	margin       = 56.0
	contentWidth = pageWidth - 2*margin
	maxImageSide = 420.0
)

/* 正文字号：标题 20、一级标题 16、二级标题 13、正文 11 */
const (
	titleSize = 20.0
	h1Size    = 16.0
	h2Size    = 13.0
	bodySize  = 11.0
)

/* Renderer 是无状态 PDF 渲染器 */
type Renderer struct{}

/* New 创建 PDF 渲染器 */
func New() *Renderer { return &Renderer{} }

/* block 是排版单元：文本行或图片 */
type block struct {
	text    string
	size    float64
	leading float64
	image   *port.PDFImage
	imageID int
}

/* page 是一页已排版的绘制指令与所用图片 */
type page struct {
	ops      []string
	imageIDs []int
}

/*
 * Render 把文档排版为 PDF。
 * 约束：仅嵌入 image/jpeg；其他格式跳过，避免生成无法解码的 XObject。
 */
func (r *Renderer) Render(ctx context.Context, document port.PDFDocument) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	/* 先过滤可嵌入图片，再排版，保证绘制指令中的图片序号与对象编号一致 */
	images := make([]port.PDFImage, 0, len(document.Images))
	for _, image := range document.Images {
		if isJPEG(image.ContentType) && len(image.Data) > 0 && image.Width > 0 && image.Height > 0 {
			images = append(images, image)
		}
	}

	blocks := layout(document.Title, document.Content, images)
	pages := paginate(blocks)
	if len(pages) == 0 {
		pages = append(pages, page{})
	}
	return writePDF(pages, images), nil
}

/* layout 把标题、Markdown 正文与图片转换为按行拆分的排版单元 */
func layout(rawTitle, content string, images []port.PDFImage) []block {
	blocks := make([]block, 0, 64)
	title := strings.TrimSpace(rawTitle)
	if title == "" {
		title = "未命名笔记"
	}
	blocks = append(blocks, textBlock(title, titleSize, titleSize*1.5))
	blocks = append(blocks, textBlock("", bodySize, bodySize*0.8))

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		size, spacing := bodySize, bodySize*1.65
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "###"):
			trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			size = h2Size
		case strings.HasPrefix(trimmed, "##"):
			trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			size = h2Size
		case strings.HasPrefix(trimmed, "#"):
			trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			size = h1Size
		case trimmed == "":
			blocks = append(blocks, textBlock("", bodySize, bodySize*0.9))
			continue
		}

		/* 按可用宽度折行；中文按全角、ASCII 按半角估算，避免长段落溢出页面 */
		for _, wrapped := range wrapLine(trimmed, size) {
			blocks = append(blocks, textBlock(wrapped, size, spacing))
		}
	}

	for index := range images {
		image := images[index]
		blocks = append(blocks, block{image: &image, imageID: index, leading: bodySize * 1.2})
	}
	return blocks
}

/* textBlock 构造一个文本排版单元 */
func textBlock(text string, size, leading float64) block {
	return block{text: text, size: size, leading: leading}
}

/* paginate 按页面高度切分排版单元，图片整体移动到下一页而不是截断 */
func paginate(blocks []block) []page {
	pages := make([]page, 0, 4)
	current := page{}
	y := pageHeight - margin

	for _, item := range blocks {
		height := item.leading
		width, imageHeight := 0.0, 0.0
		if item.image != nil {
			width, imageHeight = imageSize(*item.image)
			height = imageHeight + bodySize*1.2
		}

		if y-height < margin && y < pageHeight-margin {
			pages = append(pages, current)
			current = page{}
			y = pageHeight - margin
		}

		if item.image != nil {
			text := fmt.Sprintf("q\n%.2f 0 0 %.2f %.2f %.2f cm\n/Im%d Do\nQ", width, imageHeight,
				margin, y-imageHeight, item.imageID)
			current.ops = append(current.ops, text)
			current.imageIDs = append(current.imageIDs, item.imageID)
		} else if item.text != "" {
			current.ops = append(current.ops, textOp(item.text, item.size, margin, y-item.size))
		}
		y -= height
	}
	if len(current.ops) > 0 || len(pages) == 0 {
		pages = append(pages, current)
	}
	return pages
}

/* textOp 生成一条绝对定位的文本绘制指令，字符串使用 UTF-16BE 十六进制 */
func textOp(text string, size, x, y float64) string {
	return fmt.Sprintf("BT\n/F1 %.2f Tf\n1 0 0 1 %.2f %.2f Tm\n<%s> Tj\nET",
		size, x, y, utf16Hex(text))
}

/* utf16Hex 把文本编码为带 BOM 的 UCS-2 十六进制串，配合 UniGB-UCS2-H 使用 */
func utf16Hex(text string) string {
	units := utf16.Encode([]rune(text))
	var builder strings.Builder
	builder.Grow(4 + len(units)*4)
	builder.WriteString("FEFF")
	for _, unit := range units {
		fmt.Fprintf(&builder, "%04X", unit)
	}
	return builder.String()
}

/* wrapLine 按估算宽度折行；ASCII 计 0.5 字宽，其余按全角 1 字宽 */
func wrapLine(text string, size float64) []string {
	if text == "" {
		return nil
	}
	maxUnits := contentWidth / size
	lines := make([]string, 0, 4)
	runes := []rune(text)
	var builder strings.Builder
	used := 0.0
	for _, char := range runes {
		width := 1.0
		if char < 128 {
			width = 0.5
		}
		if used+width > maxUnits && builder.Len() > 0 {
			lines = append(lines, strings.TrimRight(builder.String(), " "))
			builder.Reset()
			used = 0
		}
		builder.WriteRune(char)
		used += width
	}
	if builder.Len() > 0 {
		lines = append(lines, strings.TrimRight(builder.String(), " "))
	}
	return lines
}

/* imageSize 计算图片按可用宽度与最大边长缩放后的尺寸 */
func imageSize(image port.PDFImage) (float64, float64) {
	width := float64(image.Width)
	height := float64(image.Height)
	scale := contentWidth / width
	if limit := maxImageSide / height; limit < scale {
		scale = limit
	}
	if scale > 1 {
		scale = 1
	}
	return width * scale, height * scale
}

/* isJPEG 判断图片是否为可直接以 DCTDecode 嵌入的 JPEG */
func isJPEG(contentType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(contentType))
	return normalized == "image/jpeg" || normalized == "image/jpg"
}

/*
 * writePDF 组装 PDF 对象、交叉引用表与文件尾。
 * 对象编号固定：1 目录、2 页面树、3 Type0 字体、4 CID 字体，之后依次为页面、内容流与图片。
 */
func writePDF(pages []page, images []port.PDFImage) []byte {
	pageCount := len(pages)
	catalogID := 1
	pagesID := 2
	fontID := 3
	firstPageID := 5
	contentID := func(index int) int { return firstPageID + pageCount + index }
	imageID := func(index int) int { return firstPageID + 2*pageCount + index }

	objects := make([]string, 0, 4+2*pageCount+len(images))
	objects = append(objects, "<< /Type /Catalog /Pages 2 0 R >>")
	objects = append(objects, "") /* 页面树稍后回填 */
	objects = append(objects,
		"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>")
	objects = append(objects,
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light "+
			"/CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /DW 1000 >>")

	/* 页面对象先占位，内容流对象随后写入，保证编号与引用一致 */
	pageBodies := make([]string, pageCount)
	for index := range pages {
		pageBodies[index] = ""
		objects = append(objects, "")
	}

	for _, current := range pages {
		stream := strings.Join(current.ops, "\n")
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}

	for _, image := range images {
		header := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB "+
			"/BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n",
			image.Width, image.Height, len(image.Data))
		objects = append(objects, header+string(image.Data)+"\nendstream")
	}

	/* 回填页面对象：资源字典只列出本页实际使用的图片 */
	for index, current := range pages {
		xobjects := strings.Builder{}
		xobjects.WriteString("<< ")
		for _, id := range current.imageIDs {
			fmt.Fprintf(&xobjects, "/Im%d %d 0 R ", id, imageID(id))
		}
		xobjects.WriteString(">>")
		pageBodies[index] = fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.2f %.2f] "+
			"/Resources << /Font << /F1 %d 0 R >> /XObject %s >> /Contents %d 0 R >>",
			pagesID, pageWidth, pageHeight, fontID, xobjects.String(), contentID(index))
		objects[firstPageID-1+index] = pageBodies[index]
	}

	kids := strings.Builder{}
	for index := 0; index < pageCount; index++ {
		fmt.Fprintf(&kids, "%d 0 R ", firstPageID+index)
	}
	objects[pagesID-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), pageCount)

	return assemble(objects, catalogID)
}

/* assemble 写入文件头、对象体、交叉引用表与文件尾，偏移量按字节累计 */
func assemble(objects []string, rootID int) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")

	offsets := make([]int, len(objects))
	for index, body := range objects {
		offsets[index] = buffer.Len()
		fmt.Fprintf(&buffer, "%d 0 obj\n%s\nendobj\n", index+1, body)
	}

	xrefOffset := buffer.Len()
	fmt.Fprintf(&buffer, "xref\n0 %d\n", len(objects)+1)
	buffer.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buffer, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buffer, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, rootID, xrefOffset)
	return buffer.Bytes()
}

/* 确保实现端口契约 */
var _ port.PDFRenderer = (*Renderer)(nil)
