/* PDF 导出：读取页面原图并按 A4 排版，文本与图片由渲染器统一处理 */
package export

import (
	"context"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* buildPDF 生成单篇 PDF；仅嵌入 JPEG 原图，其余格式由渲染器跳过 */
func (s *Service) buildPDF(ctx context.Context, userID, noteID string) ([]byte, error) {
	note, err := s.loadExportableNote(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	pages, err := s.deps.Pages.ListAll(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}

	images := make([]port.PDFImage, 0, len(pages))
	for _, page := range pages {
		info, err := s.deps.Objects.Stat(ctx, page.ObjectKey)
		if err != nil {
			return nil, errs.New(errs.CodeImageUnreadable, "页面图片不可用，请联网后重试").
				WithMeta("pageIndex", page.Index).WithCause(err)
		}
		if !isJPEG(info.ContentType) {
			continue
		}
		data, err := s.deps.Objects.Get(ctx, page.ObjectKey)
		if err != nil {
			return nil, errs.New(errs.CodeImageUnreadable, "页面图片不可用，请联网后重试").
				WithMeta("pageIndex", page.Index).WithCause(err)
		}
		images = append(images, port.PDFImage{
			Data:        data,
			Width:       page.Width,
			Height:      page.Height,
			ContentType: info.ContentType,
		})
	}

	document := port.PDFDocument{Title: note.DisplayTitle(), Content: note.Content, Images: images}
	data, err := s.deps.PDF.Render(ctx, document)
	if err != nil {
		return nil, errs.New(errs.CodeInternal, "生成 PDF 失败").WithCause(err)
	}
	return data, nil
}
