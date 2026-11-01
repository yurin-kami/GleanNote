/* 标签用例的共享辅助：鉴权前置、排序白名单、视图映射与分页令牌 */
package tag

import (
	"strings"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

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

/* parseTagOrder 校验排序白名单，缺省为笔记数倒序 */
func parseTagOrder(raw string) (port.TagOrder, error) {
	switch strings.TrimSpace(raw) {
	case "", orderByNoteCount:
		return port.TagOrderNoteCountDesc, nil
	case orderByNameAsc:
		return port.TagOrderNameAsc, nil
	default:
		return "", errs.New(errs.CodeValidationFailed, "orderBy 只支持 noteCount desc 或 name asc")
	}
}

/* tagScope 把排序方式编入令牌作用域，禁止跨排序复用 */
func tagScope(order port.TagOrder) string {
	return tagPageScopeBase + ":" + string(order)
}

/* decodeCursor 解码分页令牌；签名错误与过期统一映射为 INVALID_PAGE_TOKEN */
func (s *Service) decodeCursor(scope, token string) (*port.TagCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := s.deps.PageTokens.Decode(scope, token)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
	}
	return &port.TagCursor{SortKey: payload.SortKey, ID: payload.ID}, nil
}

/* encodeCursor 用本页最后一条标签生成下一页令牌 */
func (s *Service) encodeCursor(scope string, order port.TagOrder, last TagView) (string, error) {
	key := port.TagSortKey(order, &entity.Tag{ID: last.ID, Name: last.Name}, last.NoteCount)
	token, err := s.deps.PageTokens.Encode(scope, key, last.ID)
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
	}
	return token, nil
}

/* toTagView 把实体与源码计数映射为对外视图 */
func toTagView(tag *entity.Tag, noteCount int) TagView {
	return TagView{
		ID:        tag.ID,
		Name:      tag.Name,
		NoteCount: noteCount,
		CreatedAt: tag.CreatedAt,
	}
}

/* newID 生成标签标识；随机源异常按内部错误上抛 */
func (s *Service) newID() (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成标签 ID 失败").WithCause(err)
	}
	return id, nil
}

/* dedupeIDs 按首次出现顺序去重，避免重复标签挤占 10 个名额 */
func dedupeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
