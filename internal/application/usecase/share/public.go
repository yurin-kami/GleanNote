/* 公开读取用例：仅返回单篇笔记的标题、正文与页面图片，不含目录、标签与作者 */
package share

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * PublicNote 按令牌返回只读内容。
 * 约束：分享关闭、令牌失效、笔记进入回收站或所在目录被删除时一律返回 410 SHARE_LINK_INACTIVE，
 * 且响应不包含标题与正文（PRD FR-15、5.7）。
 * 参考：docs/API.md#59-分享、docs/ARCHITECTURE.md#66-分享页访问
 */
func (s *Service) PublicNote(ctx context.Context, token string) (PublicNoteView, error) {
	token = normalizeToken(token)
	if token == "" {
		return PublicNoteView{}, inactiveShare()
	}
	link, err := s.deps.Shares.GetByToken(ctx, token)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return PublicNoteView{}, inactiveShare()
		}
		return PublicNoteView{}, err
	}
	if !link.Enabled || link.Token != token {
		return PublicNoteView{}, inactiveShare()
	}
	note, err := s.deps.Notes.GetByID(ctx, link.UserID, link.NoteID)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return PublicNoteView{}, inactiveShare()
		}
		return PublicNoteView{}, err
	}
	if note.IsDeleted() {
		return PublicNoteView{}, inactiveShare()
	}
	folder, err := s.deps.Folders.GetByID(ctx, link.UserID, note.FolderID)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return PublicNoteView{}, inactiveShare()
		}
		return PublicNoteView{}, err
	}
	if folder.IsDeleted() {
		return PublicNoteView{}, inactiveShare()
	}

	pages, err := s.deps.Pages.ListAll(ctx, link.UserID, link.NoteID)
	if err != nil {
		return PublicNoteView{}, err
	}
	items := make([]PublicPageView, 0, len(pages))
	for _, page := range pages {
		url, _, err := s.deps.Objects.PresignGet(ctx, page.ObjectKey, publicPageURLTTL)
		if err != nil {
			return PublicNoteView{}, errs.New(errs.CodeInternal, "生成原图地址失败").WithCause(err)
		}
		items = append(items, PublicPageView{Index: page.Index, OriginalURL: url})
	}
	return PublicNoteView{
		Title:     note.DisplayTitle(),
		Content:   note.Content,
		Pages:     items,
		UpdatedAt: note.UpdatedAt,
	}, nil
}

/* inactiveShare 统一失效语义，响应体不含任何笔记内容 */
func inactiveShare() error {
	return errs.New(errs.CodeShareLinkInactive, "分享链接已失效")
}
