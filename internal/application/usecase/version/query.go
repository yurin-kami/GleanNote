/* 版本列表与详情用例：按生成时间倒序，仅返回保留期内的快照 */
package version

import (
	"context"
	"sort"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * ListVersions 返回笔记版本列表。
 * 顺序：校验笔记归属 → 过滤过期版本 → 倒序排序 → 按游标截页并签发下一页令牌。
 * 参考：docs/API.md#58-版本、PRD FR-16
 */
func (s *Service) ListVersions(ctx context.Context, userID, noteID string, pageSize int, pageToken string) (ListVersionsResult, error) {
	if err := requireUser(userID); err != nil {
		return ListVersionsResult{}, err
	}
	if noteID == "" {
		return ListVersionsResult{}, errs.New(errs.CodeValidationFailed, "noteId 必填")
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return ListVersionsResult{}, err
	}

	scope := versionScope(noteID)
	var after *struct{ sortKey, id string }
	if pageToken != "" {
		payload, err := s.deps.PageTokens.Decode(scope, pageToken)
		if err != nil {
			return ListVersionsResult{}, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
		}
		after = &struct{ sortKey, id string }{payload.SortKey, payload.ID}
	}

	all, err := s.deps.Versions.ListByNote(ctx, userID, noteID)
	if err != nil {
		return ListVersionsResult{}, err
	}
	now := s.clock.Now()
	candidates := make([]*entity.NoteVersion, 0, len(all))
	for _, version := range all {
		if !version.Expired(now) {
			candidates = append(candidates, version)
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := versionSortKey(candidates[left])
		rightKey := versionSortKey(candidates[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return candidates[left].ID > candidates[right].ID
	})
	if after != nil {
		filtered := make([]*entity.NoteVersion, 0, len(candidates))
		for _, version := range candidates {
			key := versionSortKey(version)
			if key > after.sortKey || (key == after.sortKey && version.ID < after.id) {
				filtered = append(filtered, version)
			}
		}
		candidates = filtered
	}

	size := normalizePageSize(pageSize)
	nextToken := ""
	if len(candidates) > size {
		candidates = candidates[:size]
		last := candidates[len(candidates)-1]
		nextToken, err = s.deps.PageTokens.Encode(scope, versionSortKey(last), last.ID)
		if err != nil {
			return ListVersionsResult{}, errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
		}
	}

	items := make([]VersionView, 0, len(candidates))
	for _, version := range candidates {
		items = append(items, toVersionView(version))
	}
	return ListVersionsResult{Items: items, NextPageToken: nextToken}, nil
}

/*
 * GetVersion 返回版本正文。
 * 约束：过期版本按 404 处理，避免客户端读取已清理内容。
 * 参考：docs/API.md#58-版本
 */
func (s *Service) GetVersion(ctx context.Context, userID, noteID, versionID string) (VersionDetailView, error) {
	if err := requireUser(userID); err != nil {
		return VersionDetailView{}, err
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return VersionDetailView{}, err
	}
	version, err := s.deps.Versions.GetByID(ctx, userID, noteID, versionID)
	if err != nil {
		return VersionDetailView{}, err
	}
	if version.Expired(s.clock.Now()) {
		return VersionDetailView{}, errs.New(errs.CodeResourceNotFound, "版本已过期")
	}
	return VersionDetailView{VersionView: toVersionView(version), Content: version.Content}, nil
}
