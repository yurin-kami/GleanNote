/* 增量同步查询：按游标拉取变更、过滤资源类型并判定游标过期 */
package changes

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * ListChanges 返回 since 之后的变更集。
 * 裁决：since=0 表示全量初始化，永远可回溯；其余游标一旦早于日志已清理水位即返回
 * SYNC_CURSOR_EXPIRED，客户端必须重新初始化（docs/API.md#510-增量同步）。
 */
func (s *Service) ListChanges(ctx context.Context, input ListChangesInput) (ListChangesResult, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return ListChangesResult{}, errs.New(errs.CodeValidationFailed, "缺少用户身份")
	}
	if input.Since < 0 {
		return ListChangesResult{}, errs.New(errs.CodeValidationFailed, "since 必须为非负整数")
	}
	pageSize := input.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		return ListChangesResult{}, errs.New(errs.CodeValidationFailed, "pageSize 最大为 500")
	}
	types, err := parseResourceTypes(input.ResourceTypes)
	if err != nil {
		return ListChangesResult{}, err
	}

	/* 保留期清理在读路径上顺带执行，避免为内存实现单独引入后台任务 */
	if _, err := s.deps.Log.Prune(ctx, s.clock.Now().Add(-Retention)); err != nil {
		return ListChangesResult{}, err
	}
	trimmed, err := s.deps.Log.TrimmedThrough(ctx, input.UserID)
	if err != nil {
		return ListChangesResult{}, err
	}
	if input.Since > 0 && input.Since < trimmed {
		return ListChangesResult{}, errs.New(errs.CodeSyncCursorExpired, "同步游标超出可回溯范围")
	}

	items, hasMore, err := s.deps.Log.List(ctx, input.UserID, input.Since, pageSize, types)
	if err != nil {
		return ListChangesResult{}, err
	}
	result := ListChangesResult{
		Changes:    toChangeViews(items),
		NextCursor: input.Since,
		HasMore:    hasMore,
	}
	if len(items) > 0 {
		result.NextCursor = items[len(items)-1].Seq
	}
	return result, nil
}

/* parseResourceTypes 校验并去重资源类型；缺省返回 nil 表示不过滤 */
func parseResourceTypes(raw []string) ([]entity.ChangeResourceType, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[entity.ChangeResourceType]struct{}, len(raw))
	types := make([]entity.ChangeResourceType, 0, len(raw))
	for _, item := range raw {
		value := entity.ChangeResourceType(strings.ToUpper(strings.TrimSpace(item)))
		if !entity.ValidChangeResourceType(value) {
			return nil, errs.New(errs.CodeValidationFailed, "resourceTypes 仅支持 FOLDER、NOTE、TAG、PAGE")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		types = append(types, value)
	}
	return types, nil
}

/* toChangeViews 映射变更视图；载荷保持原样，由传输层序列化 */
func toChangeViews(items []*entity.ChangeEvent) []ChangeView {
	views := make([]ChangeView, 0, len(items))
	for _, item := range items {
		views = append(views, ChangeView{
			Seq:          item.Seq,
			ResourceType: string(item.ResourceType),
			ResourceID:   item.ResourceID,
			Op:           string(item.Op),
			Revision:     item.Revision,
			UpdatedAt:    item.UpdatedAt,
			Payload:      item.Payload,
		})
	}
	return views
}
