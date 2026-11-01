/* 目录读取用例：分页列表、详情与面包屑 */
package folder

import (
	"context"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
)

/*
 * ListFolders 返回某父级下的未删除子目录。
 * 顺序：校验排序 → 校验父级可见 → 解码游标 → 多取一条判断下一页。
 * 参考：docs/API.md#522-get-v1folders、docs/API.md#24-分页
 */
func (s *Service) ListFolders(ctx context.Context, input ListFoldersInput) (ListFoldersResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return ListFoldersResult{}, err
	}
	order, err := parseOrder(input.OrderBy)
	if err != nil {
		return ListFoldersResult{}, err
	}
	size := normalizePageSize(input.PageSize)

	var parentID *string
	if input.ParentID != "" {
		parent, err := s.activeFolder(ctx, input.UserID, input.ParentID)
		if err != nil {
			return ListFoldersResult{}, err
		}
		parentID = parentIDOf(parent)
	}

	scope := folderScope(input.ParentID, order)
	cursor, err := s.decodeCursor(scope, input.PageToken)
	if err != nil {
		return ListFoldersResult{}, err
	}
	items, total, err := s.deps.Folders.ListChildren(ctx, port.FolderListQuery{
		UserID:   input.UserID,
		ParentID: parentID,
		Order:    order,
		Limit:    size + 1,
		After:    cursor,
	})
	if err != nil {
		return ListFoldersResult{}, err
	}

	nextToken := ""
	if len(items) > size {
		items = items[:size]
		nextToken, err = s.nextPageToken(scope, order, items[len(items)-1])
		if err != nil {
			return ListFoldersResult{}, err
		}
	}
	views := make([]FolderView, 0, len(items))
	for _, item := range items {
		views = append(views, toView(item))
	}
	if err := s.fillChildCounts(ctx, input.UserID, views); err != nil {
		return ListFoldersResult{}, err
	}
	if err := s.fillNoteCounts(ctx, input.UserID, views); err != nil {
		return ListFoldersResult{}, err
	}
	return ListFoldersResult{Items: views, NextPageToken: nextToken, TotalSize: total}, nil
}

/* GetFolder 返回单个目录详情；已删除目录按 404 处理 */
func (s *Service) GetFolder(ctx context.Context, userID, folderID string) (FolderView, error) {
	if err := requireUser(userID); err != nil {
		return FolderView{}, err
	}
	folder, err := s.activeFolder(ctx, userID, folderID)
	if err != nil {
		return FolderView{}, err
	}
	view := toView(folder)
	view.ChildCount, err = s.countChildren(ctx, userID, folder.ID)
	if err != nil {
		return FolderView{}, err
	}
	view.NoteCount, err = s.countNotes(ctx, userID, folder.ID)
	if err != nil {
		return FolderView{}, err
	}
	return view, nil
}

/*
 * FolderPath 返回从根级到当前目录的面包屑。
 * 沿 ParentID 上溯，最多 5 次查询；祖先缺失视为数据不一致，按 404 处理。
 * 参考：docs/API.md#524-get-v1foldersfolderidpath
 */
func (s *Service) FolderPath(ctx context.Context, userID, folderID string) (FolderPathView, error) {
	if err := requireUser(userID); err != nil {
		return FolderPathView{}, err
	}
	current, err := s.activeFolder(ctx, userID, folderID)
	if err != nil {
		return FolderPathView{}, err
	}
	chain := make([]FolderPathItem, 0, current.Depth)
	for {
		chain = append(chain, FolderPathItem{ID: current.ID, Name: current.Name})
		if current.ParentID == nil {
			break
		}
		parent, err := s.activeFolder(ctx, userID, *current.ParentID)
		if err != nil {
			if errs.IsCode(err, errs.CodeResourceNotFound) {
				return FolderPathView{}, errs.New(errs.CodeResourceNotFound, "目录路径不完整")
			}
			return FolderPathView{}, err
		}
		current = parent
	}

	items := make([]FolderPathItem, 0, len(chain))
	names := make([]string, 0, len(chain))
	for index := len(chain) - 1; index >= 0; index-- {
		items = append(items, chain[index])
		names = append(names, chain[index].Name)
	}
	return FolderPathView{Items: items, DisplayPath: service.DisplayPath(names)}, nil
}
