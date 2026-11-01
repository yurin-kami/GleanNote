/* 回收站读取用例：按删除批次聚合目录与笔记，键集分页 */
package trash

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
)

/* trashSortKey 把删除时间倒序编码为升序键，批次 ID 用于同刻稳定排序 */
func trashSortKey(deletedAt time.Time) string {
	return fmt.Sprintf("%019d", math.MaxInt64-deletedAt.UnixNano())
}

/*
 * ListTrash 返回回收站批次列表。
 * 顺序：校验参数 → 解码游标 → 读取两张回收站表 → 按批次聚合 → 删除时间倒序 → 截页与签发令牌。
 * 参考：docs/API.md#56-回收站、docs/API.md#25-排序与过滤
 */
func (s *Service) ListTrash(ctx context.Context, input ListTrashInput) (ListTrashResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return ListTrashResult{}, err
	}
	size := normalizePageSize(input.PageSize)
	cursor, err := s.decodeCursor(input.PageToken)
	if err != nil {
		return ListTrashResult{}, err
	}

	folders, err := s.deps.Folders.ListDeleted(ctx, input.UserID)
	if err != nil {
		return ListTrashResult{}, err
	}
	notes, err := s.deps.Notes.ListDeleted(ctx, input.UserID)
	if err != nil {
		return ListTrashResult{}, err
	}

	/* 目录删除批次包含整棵子树与其内笔记，笔记单独删除时批次内只有一条笔记 */
	batches := make(map[string]*batch)
	for _, folder := range folders {
		if folder.DeleteBatchID == "" {
			continue
		}
		current := batches[folder.DeleteBatchID]
		if current == nil {
			current = &batch{id: folder.DeleteBatchID}
			batches[folder.DeleteBatchID] = current
		}
		current.folders = append(current.folders, folder)
	}
	for _, note := range notes {
		if note.DeleteBatchID == "" {
			continue
		}
		current := batches[note.DeleteBatchID]
		if current == nil {
			current = &batch{id: note.DeleteBatchID}
			batches[note.DeleteBatchID] = current
		}
		current.notes = append(current.notes, note)
	}

	entries := make([]trashEntry, 0, len(batches))
	for _, current := range batches {
		view, ok, err := s.buildItem(ctx, input.UserID, current)
		if err != nil {
			return ListTrashResult{}, err
		}
		if !ok {
			continue
		}
		entries = append(entries, trashEntry{view: view, key: trashSortKey(view.DeletedAt)})
	}
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].key != entries[right].key {
			return entries[left].key < entries[right].key
		}
		return entries[left].view.BatchID < entries[right].view.BatchID
	})

	if cursor != nil {
		filtered := make([]trashEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.key > cursor.SortKey || (entry.key == cursor.SortKey && entry.view.BatchID > cursor.ID) {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}

	nextToken := ""
	if len(entries) > size {
		entries = entries[:size]
		last := entries[len(entries)-1]
		token, err := s.deps.PageTokens.Encode(trashPageScope, last.key, last.view.BatchID)
		if err != nil {
			return ListTrashResult{}, errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
		}
		nextToken = token
	}
	items := make([]TrashItemView, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.view)
	}
	return ListTrashResult{Items: items, NextPageToken: nextToken}, nil
}

/* batch 是同一删除批次内的目录与笔记集合 */
type batch struct {
	id      string
	folders []*entity.Folder
	notes   []*entity.Note
}

/* trashEntry 把排序键与视图绑定，避免排序时反复计算 */
type trashEntry struct {
	view TrashItemView
	key  string
}

/*
 * buildItem 把批次聚合为一条回收站视图。
 * 目录批次取子树根节点作为 itemId 与 name；纯笔记批次取该笔记。
 */
func (s *Service) buildItem(ctx context.Context, userID string, current *batch) (TrashItemView, bool, error) {
	view := TrashItemView{
		BatchID:     current.id,
		FolderCount: len(current.folders),
		NoteCount:   len(current.notes),
	}
	if len(current.folders) > 0 {
		root := rootFolder(current.folders)
		if root.DeletedAt == nil || root.PurgeAt == nil {
			return TrashItemView{}, false, nil
		}
		path, err := s.folderPath(ctx, userID, root.ID)
		if err != nil {
			return TrashItemView{}, false, err
		}
		view.ItemType = TrashItemFolder
		view.ItemID = root.ID
		view.Name = root.Name
		view.OriginalPath = path
		view.DeletedAt = *root.DeletedAt
		view.PurgeAt = *root.PurgeAt
	} else if len(current.notes) > 0 {
		note := current.notes[0]
		if note.DeletedAt == nil || note.PurgeAt == nil {
			return TrashItemView{}, false, nil
		}
		path, err := s.folderPath(ctx, userID, note.FolderID)
		if err != nil {
			return TrashItemView{}, false, err
		}
		view.ItemType = TrashItemNote
		view.ItemID = note.ID
		view.Name = note.DisplayTitle()
		view.OriginalPath = path
		view.DeletedAt = *note.DeletedAt
		view.PurgeAt = *note.PurgeAt
	} else {
		return TrashItemView{}, false, nil
	}
	view.RemainingDays = remainingDays(s.clock.Now(), view.PurgeAt)
	return view, true, nil
}

/* rootFolder 返回批次内未被同批次父级包含的根节点；异常批次退化为最浅节点 */
func rootFolder(folders []*entity.Folder) *entity.Folder {
	inBatch := make(map[string]struct{}, len(folders))
	for _, folder := range folders {
		inBatch[folder.ID] = struct{}{}
	}
	var chosen *entity.Folder
	for _, folder := range folders {
		parentOutside := folder.ParentID == nil
		if folder.ParentID != nil {
			if _, sameBatch := inBatch[*folder.ParentID]; !sameBatch {
				parentOutside = true
			}
		}
		if !parentOutside {
			continue
		}
		if chosen == nil || folder.Depth < chosen.Depth ||
			(folder.Depth == chosen.Depth && folder.ID < chosen.ID) {
			chosen = folder
		}
	}
	if chosen != nil {
		return chosen
	}
	chosen = folders[0]
	for _, folder := range folders[1:] {
		if folder.Depth < chosen.Depth || (folder.Depth == chosen.Depth && folder.ID < chosen.ID) {
			chosen = folder
		}
	}
	return chosen
}

/* folderPath 返回从根级到目标目录的展示路径；缺失祖先或目录已彻底删除时返回空串 */
func (s *Service) folderPath(ctx context.Context, userID, folderID string) (string, error) {
	if folderID == "" {
		return "", nil
	}
	current, err := s.deps.Folders.GetByID(ctx, userID, folderID)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return "", nil
		}
		return "", err
	}
	names := make([]string, 0, current.Depth)
	for {
		names = append(names, current.Name)
		if current.ParentID == nil {
			break
		}
		parent, err := s.deps.Folders.GetByID(ctx, userID, *current.ParentID)
		if err != nil {
			if errs.IsCode(err, errs.CodeResourceNotFound) {
				break
			}
			return "", err
		}
		current = parent
	}
	ordered := make([]string, 0, len(names))
	for index := len(names) - 1; index >= 0; index-- {
		ordered = append(ordered, names[index])
	}
	return service.DisplayPath(ordered), nil
}

/* remainingDays 计算剩余保留天数，向上取整且最小为 0 */
func remainingDays(now, purgeAt time.Time) int {
	if !purgeAt.After(now) {
		return 0
	}
	day := 24 * time.Hour
	remaining := purgeAt.Sub(now)
	days := int(remaining / day)
	if remaining%day != 0 {
		days++
	}
	return days
}

/* requireUser 拒绝缺失访问令牌上下文的调用，语义与认证模块一致 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
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

/* decodeCursor 解码分页令牌；签名错误与过期统一映射为 INVALID_PAGE_TOKEN */
func (s *Service) decodeCursor(token string) (*port.NoteCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := s.deps.PageTokens.Decode(trashPageScope, token)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
	}
	return &port.NoteCursor{SortKey: payload.SortKey, ID: payload.ID}, nil
}
