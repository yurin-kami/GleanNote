/* 笔记用例的共享辅助：鉴权前置、视图映射、目录路径与分页令牌 */
package note

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/service"
)

/* requireUser 拒绝缺失访问令牌上下文的调用，语义与认证模块一致 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
}

/*
 * activeFolder 读取未删除目录；已删除或缺失按 404 处理，禁止在回收站目录内新建笔记。
 * 参考：docs/API.md#533-post-v1notes
 */
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

/*
 * ensureNoActiveJob 拒绝同一笔记重复提交转换任务。
 * 两个任务同时处理同一篇笔记会争用正文与页面序号，并重复扣减额度。
 */
func (s *Service) ensureNoActiveJob(ctx context.Context, userID, noteID string) error {
	active, err := s.deps.Jobs.ListActiveByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, job := range active {
		if job.NoteID == noteID {
			return errs.New(errs.CodeConversionStateInvalid, "该笔记已有进行中的转换任务")
		}
	}
	return nil
}

/* toView 把实体映射为对外视图；正文与分享状态由详情接口按需输出 */
func toView(note *entity.Note, folderPath string) NoteView {
	return NoteView{
		ID:             note.ID,
		FolderID:       note.FolderID,
		FolderPath:     folderPath,
		Title:          note.Title,
		State:          string(note.State),
		Revision:       note.Revision,
		CharCount:      note.CharCount,
		IsConflictCopy: note.ConflictCopy,
		Content:        note.Content,
		CreatedAt:      note.CreatedAt,
		UpdatedAt:      note.UpdatedAt,
	}
}

/*
 * folderPath 返回从根级到笔记所在目录的展示路径。
 * 已彻底删除的目录或缺失祖先按空路径处理，保证回收站内的笔记仍可读取。
 */
func (s *Service) folderPath(ctx context.Context, userID, folderID string) (string, error) {
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

/*
 * buildView 读取目录路径并生成视图，再批量回填页面数与缩略图地址。
 * 路径或页面缺失都不影响笔记本身可读；缩略图取首页（Index 最小）的派生图。
 */
func (s *Service) buildView(ctx context.Context, userID string, note *entity.Note) (NoteView, error) {
	path, err := s.folderPath(ctx, userID, note.FolderID)
	if err != nil {
		return NoteView{}, err
	}
	views := []NoteView{toView(note, path)}
	if err := s.fillPageSummary(ctx, userID, views); err != nil {
		return NoteView{}, err
	}
	if err := s.fillTags(ctx, userID, views); err != nil {
		return NoteView{}, err
	}
	if err := s.fillShares(ctx, userID, views); err != nil {
		return NoteView{}, err
	}
	return views[0], nil
}

/*
 * fillShares 回填详情中的 shareEnabled，避免笔记模块依赖分享模块的实现。
 * 分享状态只影响详情输出，列表结构不含该字段。
 */
func (s *Service) fillShares(ctx context.Context, userID string, views []NoteView) error {
	if len(views) == 0 {
		return nil
	}
	noteIDs := make([]string, 0, len(views))
	for _, view := range views {
		noteIDs = append(noteIDs, view.ID)
	}
	enabled, err := s.deps.Shares.EnabledByNotes(ctx, userID, noteIDs)
	if err != nil {
		return err
	}
	for index := range views {
		views[index].ShareEnabled = enabled[views[index].ID]
	}
	return nil
}

/*
 * fillTags 批量回填笔记标签，避免列表逐条查询关联表。
 * 标签顺序沿用设置时的顺序，重命名由标签用例统一维护（docs/API.md#57-标签）。
 */
func (s *Service) fillTags(ctx context.Context, userID string, views []NoteView) error {
	if len(views) == 0 {
		return nil
	}
	noteIDs := make([]string, 0, len(views))
	for _, view := range views {
		noteIDs = append(noteIDs, view.ID)
	}
	grouped, err := s.deps.TagLinks.TagsByNotes(ctx, userID, noteIDs)
	if err != nil {
		return err
	}
	for index := range views {
		for _, tag := range grouped[views[index].ID] {
			views[index].Tags = append(views[index].Tags, TagView{ID: tag.ID, Name: tag.Name})
		}
	}
	return nil
}

/*
 * fillPageSummary 批量回填 pageCount 与 thumbnailUrl，避免列表逐条查询。
 * 缩略图地址 10 分钟有效（docs/API.md#29-图片上传协议）；签发失败按内部错误上抛。
 */
func (s *Service) fillPageSummary(ctx context.Context, userID string, views []NoteView) error {
	if len(views) == 0 {
		return nil
	}
	noteIDs := make([]string, 0, len(views))
	for _, view := range views {
		noteIDs = append(noteIDs, view.ID)
	}
	counts, err := s.deps.Pages.CountByNotes(ctx, userID, noteIDs)
	if err != nil {
		return err
	}
	firstPages, err := s.deps.Pages.FirstByNotes(ctx, userID, noteIDs)
	if err != nil {
		return err
	}
	for index := range views {
		views[index].PageCount = counts[views[index].ID]
		page := firstPages[views[index].ID]
		if page == nil || page.ThumbKey == "" {
			continue
		}
		url, _, err := s.deps.Objects.PresignGet(ctx, page.ThumbKey, thumbnailURLTTL)
		if err != nil {
			return errs.New(errs.CodeInternal, "生成缩略图地址失败").WithCause(err)
		}
		views[index].ThumbnailURL = url
	}
	return nil
}

/* parseState 校验状态过滤值；空串返回 nil 表示排除回收站 */
func parseState(raw string) (*entity.NoteState, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return nil, nil
	case string(entity.NoteStateDraft), string(entity.NoteStateNormal), string(entity.NoteStateDeleted):
		state := entity.NoteState(strings.TrimSpace(raw))
		return &state, nil
	default:
		return nil, errs.New(errs.CodeValidationFailed, "state 只支持 DRAFT、NORMAL、DELETED")
	}
}

/* parseOrder 校验排序白名单，缺省为更新时间倒序 */
func parseOrder(raw string) (port.NoteOrder, error) {
	switch strings.TrimSpace(raw) {
	case "", string(port.NoteOrderUpdatedDesc):
		return port.NoteOrderUpdatedDesc, nil
	case string(port.NoteOrderCreatedDesc):
		return port.NoteOrderCreatedDesc, nil
	case string(port.NoteOrderTitleAsc):
		return port.NoteOrderTitleAsc, nil
	default:
		return "", errs.New(errs.CodeValidationFailed,
			"orderBy 只支持 updateTime desc、createTime desc 或 title asc")
	}
}

/* parseUpdatedAfter 解析增量刷新时间；空串表示不过滤 */
func parseUpdatedAfter(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, errs.New(errs.CodeValidationFailed, "updatedAfter 必须为 RFC 3339 时间").WithCause(err)
	}
	return &parsed, nil
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

/* noteScope 把目录、状态、排序与时间过滤编入令牌作用域，禁止跨查询复用 */
func noteScope(folderID string, state *entity.NoteState, order port.NoteOrder, updatedAfter *time.Time) string {
	stateKey := "all"
	if state != nil {
		stateKey = string(*state)
	}
	timeKey := "none"
	if updatedAfter != nil {
		timeKey = updatedAfter.UTC().Format(time.RFC3339Nano)
	}
	return notePageScopeBase + ":" + folderID + ":" + stateKey + ":" + string(order) + ":" + timeKey
}

/* decodeCursor 解码分页令牌；签名错误与过期统一映射为 INVALID_PAGE_TOKEN */
func (s *Service) decodeCursor(scope, token string) (*port.NoteCursor, error) {
	if token == "" {
		return nil, nil
	}
	payload, err := s.deps.PageTokens.Decode(scope, token)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidPageToken, "分页令牌无效或已过期").WithCause(err)
	}
	return &port.NoteCursor{SortKey: payload.SortKey, ID: payload.ID}, nil
}

/* nextPageToken 用本页最后一条记录生成下一页令牌 */
func (s *Service) nextPageToken(scope string, order port.NoteOrder, last *entity.Note) (string, error) {
	token, err := s.deps.PageTokens.Encode(scope, port.NoteSortKey(order, last), last.ID)
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
	}
	return token, nil
}

/* newID 生成笔记或删除批次标识；随机源异常按内部错误上抛 */
func (s *Service) newID(label string) (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成"+label+" ID 失败").WithCause(err)
	}
	return id, nil
}
