/* 笔记读取用例：目录内分页列表与详情 */
package note

import (
	"context"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * ListNotes 返回目录内笔记。
 * 顺序：校验参数 → 校验目录可见 → 解码游标 → 多取一条判断下一页 → 一次性补目录路径。
 * 参考：docs/API.md#532-get-v1notes、docs/API.md#24-分页
 */
func (s *Service) ListNotes(ctx context.Context, input ListNotesInput) (ListNotesResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return ListNotesResult{}, err
	}
	if input.FolderID == "" {
		return ListNotesResult{}, errs.New(errs.CodeValidationFailed, "folderId 必填")
	}
	state, err := parseState(input.State)
	if err != nil {
		return ListNotesResult{}, err
	}
	order, err := parseOrder(input.OrderBy)
	if err != nil {
		return ListNotesResult{}, err
	}
	updatedAfter, err := parseUpdatedAfter(input.UpdatedAfter)
	if err != nil {
		return ListNotesResult{}, err
	}
	if _, err := s.activeFolder(ctx, input.UserID, input.FolderID); err != nil {
		return ListNotesResult{}, err
	}
	size := normalizePageSize(input.PageSize)

	scope := noteScope(input.FolderID, state, order, updatedAfter)
	cursor, err := s.decodeCursor(scope, input.PageToken)
	if err != nil {
		return ListNotesResult{}, err
	}
	items, total, err := s.deps.Notes.List(ctx, port.NoteListQuery{
		UserID:       input.UserID,
		FolderID:     input.FolderID,
		State:        state,
		UpdatedAfter: updatedAfter,
		Order:        order,
		Limit:        size + 1,
		After:        cursor,
	})
	if err != nil {
		return ListNotesResult{}, err
	}

	nextToken := ""
	if len(items) > size {
		items = items[:size]
		nextToken, err = s.nextPageToken(scope, order, items[len(items)-1])
		if err != nil {
			return ListNotesResult{}, err
		}
	}

	/* 同一目录的笔记共享路径，路径查询只做一次而不是逐条上溯 */
	path, err := s.folderPath(ctx, input.UserID, input.FolderID)
	if err != nil {
		return ListNotesResult{}, err
	}
	views := make([]NoteView, 0, len(items))
	for _, item := range items {
		views = append(views, toView(item, path))
	}
	/* 页面数与缩略图批量回填，避免逐条查询页面表 */
	if err := s.fillPageSummary(ctx, input.UserID, views); err != nil {
		return ListNotesResult{}, err
	}
	if err := s.fillTags(ctx, input.UserID, views); err != nil {
		return ListNotesResult{}, err
	}
	return ListNotesResult{Items: views, NextPageToken: nextToken, TotalSize: total}, nil
}

/*
 * GetNote 返回笔记详情；回收站中的笔记仍可读（state=DELETED），彻底删除返回 404。
 * 参考：docs/API.md#535-get-v1notesnoteid
 */
func (s *Service) GetNote(ctx context.Context, userID, noteID string) (NoteView, error) {
	if err := requireUser(userID); err != nil {
		return NoteView{}, err
	}
	note, err := s.deps.Notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return NoteView{}, err
	}
	return s.buildView(ctx, userID, note)
}
