/* 页面读取用例：缩略图列表与单页原图地址 */
package upload

import (
	"context"
	"strconv"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * ListPages 返回笔记页面缩略图列表。
 * 顺序：校验笔记归属 → 解码游标 → 多取一条判断下一页 → 批量签发缩略图地址。
 * 参考：docs/API.md#543-get-v1notesnoteidpages
 */
func (s *Service) ListPages(ctx context.Context, userID, noteID string, pageSize int, pageToken string) (ListPagesResult, error) {
	if err := requireUser(userID); err != nil {
		return ListPagesResult{}, err
	}
	if noteID == "" {
		return ListPagesResult{}, errs.New(errs.CodeValidationFailed, "noteId 必填")
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return ListPagesResult{}, err
	}

	scope := pageScope(noteID)
	cursor, err := s.decodeCursor(scope, pageToken)
	if err != nil {
		return ListPagesResult{}, err
	}
	size := normalizePageSize(pageSize)
	pages, err := s.deps.Pages.List(ctx, port.NotePageQuery{
		UserID: userID,
		NoteID: noteID,
		Limit:  size + 1,
		After:  cursor,
	})
	if err != nil {
		return ListPagesResult{}, err
	}

	nextToken := ""
	if len(pages) > size {
		pages = pages[:size]
		nextToken, err = s.nextPageToken(scope, pages[len(pages)-1])
		if err != nil {
			return ListPagesResult{}, err
		}
	}

	items := make([]PageView, 0, len(pages))
	for _, page := range pages {
		thumbURL, _, err := s.deps.Objects.PresignGet(ctx, page.ThumbKey, MediaURLTTL)
		if err != nil {
			return ListPagesResult{}, errs.New(errs.CodeInternal, "生成缩略图地址失败").WithCause(err)
		}
		items = append(items, PageView{
			ID:        page.ID,
			Index:     page.Index,
			Width:     page.Width,
			Height:    page.Height,
			Bytes:     page.Bytes,
			ThumbURL:  thumbURL,
			CreatedAt: page.CreatedAt,
		})
	}
	return ListPagesResult{Items: items, NextPageToken: nextToken}, nil
}

/*
 * GetPage 返回单页原图地址，有效期 10 分钟。
 * 参考：docs/API.md#544-get-v1notesnoteidpagespageid
 */
func (s *Service) GetPage(ctx context.Context, userID, noteID, pageID string) (PageDetail, error) {
	if err := requireUser(userID); err != nil {
		return PageDetail{}, err
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return PageDetail{}, err
	}
	page, err := s.deps.Pages.GetByID(ctx, userID, noteID, pageID)
	if err != nil {
		return PageDetail{}, err
	}
	url, _, err := s.deps.Objects.PresignGet(ctx, page.ObjectKey, MediaURLTTL)
	if err != nil {
		return PageDetail{}, errs.New(errs.CodeInternal, "生成原图地址失败").WithCause(err)
	}
	return PageDetail{
		ID:          page.ID,
		Index:       page.Index,
		OriginalURL: url,
		ExpiresAt:   s.clock.Now().Add(MediaURLTTL),
	}, nil
}

/* pageScope 把笔记 ID 编入令牌作用域，禁止跨笔记复用游标 */
func pageScope(noteID string) string {
	return pageScopeBase + ":" + noteID
}

/* decodeCursor 解码页面分页令牌；签名错误与过期统一映射为 INVALID_PAGE_TOKEN */
func (s *Service) decodeCursor(scope, token string) (*port.NotePageCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := s.deps.PageTokens.Decode(scope, token)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
	}
	index, err := strconv.Atoi(payload.SortKey)
	if err != nil || index <= 0 {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期")
	}
	return &port.NotePageCursor{Index: index, ID: payload.ID}, nil
}

/* nextPageToken 用本页最后一页生成下一页令牌 */
func (s *Service) nextPageToken(scope string, last *entity.NotePage) (string, error) {
	token, err := s.deps.PageTokens.Encode(scope, strconv.Itoa(last.Index), last.ID)
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
	}
	return token, nil
}

/* normalizePageSize 收敛分页大小；负数已由传输层拦截，这里兜底为默认值 */
func normalizePageSize(size int) int {
	if size <= 0 {
		return defaultPageSize
	}
	if size > maxPageSize {
		return maxPageSize
	}
	return size
}
