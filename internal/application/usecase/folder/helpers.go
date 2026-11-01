/* 目录用例的共享辅助：鉴权前置、视图映射、分页与令牌作用域 */
package folder

import (
	"context"
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

/* activeFolder 读取未删除目录；已删除节点按 404 处理，避免泄露回收站内容 */
func (s *Service) activeFolder(ctx context.Context, userID, folderID string) (*entity.Folder, error) {
	folder, err := s.deps.Folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}
	if folder.IsDeleted() {
		return nil, errs.New(errs.CodeResourceNotFound, "目录不存在")
	}
	return folder, nil
}

/* toView 把实体映射为对外视图；子目录数由调用方按需填充 */
func toView(folder *entity.Folder) FolderView {
	return FolderView{
		ID:        folder.ID,
		ParentID:  folder.ParentID,
		Name:      folder.Name,
		Depth:     folder.Depth,
		Revision:  folder.Revision,
		CreatedAt: folder.CreatedAt,
		UpdatedAt: folder.UpdatedAt,
	}
}

/* countChildren 读取单个目录的直接子目录数 */
func (s *Service) countChildren(ctx context.Context, userID, folderID string) (int, error) {
	counts, err := s.deps.Folders.CountChildren(ctx, userID, []string{folderID})
	if err != nil {
		return 0, err
	}
	return counts[folderID], nil
}

/* fillChildCounts 批量回填子目录数，避免列表接口逐条查询 */
func (s *Service) fillChildCounts(ctx context.Context, userID string, views []FolderView) error {
	if len(views) == 0 {
		return nil
	}
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	counts, err := s.deps.Folders.CountChildren(ctx, userID, ids)
	if err != nil {
		return err
	}
	for index := range views {
		views[index].ChildCount = counts[views[index].ID]
	}
	return nil
}

/* countNotes 读取单个目录内未删除笔记数 */
func (s *Service) countNotes(ctx context.Context, userID, folderID string) (int, error) {
	counts, err := s.deps.Notes.CountActiveByFolder(ctx, userID, []string{folderID})
	if err != nil {
		return 0, err
	}
	return counts[folderID], nil
}

/* fillNoteCounts 批量回填目录内笔记数，与子目录数同样只查一次 */
func (s *Service) fillNoteCounts(ctx context.Context, userID string, views []FolderView) error {
	if len(views) == 0 {
		return nil
	}
	ids := make([]string, 0, len(views))
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	counts, err := s.deps.Notes.CountActiveByFolder(ctx, userID, ids)
	if err != nil {
		return err
	}
	for index := range views {
		views[index].NoteCount = counts[views[index].ID]
	}
	return nil
}

/* countNotesInTree 汇总一组目录内未删除笔记数，用于移动与删除摘要 */
func (s *Service) countNotesInTree(ctx context.Context, userID string, nodes []*entity.Folder) (int, error) {
	if len(nodes) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	counts, err := s.deps.Notes.CountActiveByFolder(ctx, userID, ids)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, count := range counts {
		total += count
	}
	return total, nil
}

/* parseOrder 校验排序白名单，缺省为创建时间升序 */
func parseOrder(raw string) (port.FolderOrder, error) {
	switch strings.TrimSpace(raw) {
	case "", string(port.FolderOrderCreatedAtAsc):
		return port.FolderOrderCreatedAtAsc, nil
	case string(port.FolderOrderNameAsc):
		return port.FolderOrderNameAsc, nil
	default:
		return "", errs.New(errs.CodeValidationFailed, "orderBy 只支持 createTime asc 或 name asc")
	}
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

/* folderScope 把父级与排序编入令牌作用域，禁止跨查询复用分页令牌 */
func folderScope(parentID string, order port.FolderOrder) string {
	parent := parentID
	if parent == "" {
		parent = "root"
	}
	return folderPageScopeBase + ":" + parent + ":" + string(order)
}

/* decodeCursor 解码分页令牌；签名错误与过期统一映射为 INVALID_PAGE_TOKEN */
func (s *Service) decodeCursor(scope, token string) (*port.FolderCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := s.deps.PageTokens.Decode(scope, token)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
	}
	return &port.FolderCursor{SortKey: payload.SortKey, ID: payload.ID}, nil
}

/* nextPageToken 用本页最后一条记录生成下一页令牌 */
func (s *Service) nextPageToken(scope string, order port.FolderOrder, last *entity.Folder) (string, error) {
	token, err := s.deps.PageTokens.Encode(scope, port.FolderSortKey(order, last), last.ID)
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
	}
	return token, nil
}

/* newID 生成目录或删除批次标识；随机源异常按内部错误上抛 */
func (s *Service) newID() (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成目录 ID 失败").WithCause(err)
	}
	return id, nil
}

/* parentIDOf 把父实体转换为可空外键，nil 表示根级 */
func parentIDOf(parent *entity.Folder) *string {
	if parent == nil {
		return nil
	}
	id := parent.ID
	return &id
}

/* folderIDsOf 提取一组目录的标识，供笔记批量操作使用 */
func folderIDsOf(nodes []*entity.Folder) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}
