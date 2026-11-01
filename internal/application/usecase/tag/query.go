/* 标签读取用例：账号级标签列表（键集分页与笔记数排序） */
package tag

import (
	"context"
	"sort"

	"github.com/gleannote/gleannote/internal/application/port"
)

/*
 * ListTags 返回账号全部标签。
 * 顺序：校验参数 → 解码游标 → 读取标签与笔记数 → 按白名单排序 → 过滤游标 → 截页与签发令牌。
 * 参考：docs/API.md#57-标签、docs/API.md#24-分页
 */
func (s *Service) ListTags(ctx context.Context, input ListTagsInput) (ListTagsResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return ListTagsResult{}, err
	}
	order, err := parseTagOrder(input.OrderBy)
	if err != nil {
		return ListTagsResult{}, err
	}
	size := normalizePageSize(input.PageSize)
	scope := tagScope(order)
	cursor, err := s.decodeCursor(scope, input.PageToken)
	if err != nil {
		return ListTagsResult{}, err
	}

	tags, err := s.deps.Tags.List(ctx, input.UserID)
	if err != nil {
		return ListTagsResult{}, err
	}
	tagIDs := make([]string, 0, len(tags))
	for _, tag := range tags {
		tagIDs = append(tagIDs, tag.ID)
	}
	counts, err := s.deps.NoteTags.CountsByTags(ctx, input.UserID, tagIDs)
	if err != nil {
		return ListTagsResult{}, err
	}

	entries := make([]tagEntry, 0, len(tags))
	for _, tag := range tags {
		count := counts[tag.ID]
		entries = append(entries, tagEntry{view: toTagView(tag, count), key: port.TagSortKey(order, tag, count)})
	}
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].key != entries[right].key {
			return entries[left].key < entries[right].key
		}
		return entries[left].view.ID < entries[right].view.ID
	})

	/* totalSize 为过滤游标前的总数，客户端不得据此计算页数（AIP-158） */
	total := len(entries)
	if cursor != nil {
		filtered := make([]tagEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.key > cursor.SortKey || (entry.key == cursor.SortKey && entry.view.ID > cursor.ID) {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}

	nextToken := ""
	if len(entries) > size {
		entries = entries[:size]
		nextToken, err = s.encodeCursor(scope, order, entries[len(entries)-1].view)
		if err != nil {
			return ListTagsResult{}, err
		}
	}
	views := make([]TagView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, entry.view)
	}
	return ListTagsResult{Items: views, NextPageToken: nextToken, TotalSize: total}, nil
}

/* tagEntry 把排序键与视图绑定，避免排序时反复计算 */
type tagEntry struct {
	view TagView
	key  string
}
