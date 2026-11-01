/* 分享开关用例：查询状态、开启与关闭 */
package share

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/random"
)

/*
 * GetShareLink 返回笔记分享状态。
 * 从未开启过分享时返回 enabled=false，笔记本身不存在才返回 404。
 * 参考：docs/API.md#59-分享
 */
func (s *Service) GetShareLink(ctx context.Context, userID, noteID string) (ShareLinkView, error) {
	if err := requireUser(userID); err != nil {
		return ShareLinkView{}, err
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return ShareLinkView{}, err
	}
	link, err := s.deps.Shares.GetByNote(ctx, userID, noteID)
	if err != nil {
		if errs.IsCode(err, errs.CodeResourceNotFound) {
			return ShareLinkView{}, nil
		}
		return ShareLinkView{}, err
	}
	return s.toView(link), nil
}

/*
 * EnableShare 开启分享并返回公开地址。
 * 约束：仅 state=NORMAL 的笔记可分享，草稿与回收站笔记返回 SHARE_NOTE_NOT_READY；
 * 已开启时返回原地址，不重复生成令牌。
 * 参考：docs/API.md#59-分享、PRD FR-15
 */
func (s *Service) EnableShare(ctx context.Context, userID, noteID string) (ShareLinkView, error) {
	if err := requireUser(userID); err != nil {
		return ShareLinkView{}, err
	}
	note, err := s.deps.Notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return ShareLinkView{}, err
	}
	if note.State != entity.NoteStateNormal {
		return ShareLinkView{}, errs.New(errs.CodeShareNoteNotReady, "只有正常状态的笔记可以分享")
	}
	link, err := s.deps.Shares.GetByNote(ctx, userID, noteID)
	if err != nil && !errs.IsCode(err, errs.CodeResourceNotFound) {
		return ShareLinkView{}, err
	}
	if link != nil && link.Enabled && link.Token != "" {
		return s.toView(link), nil
	}

	/* 令牌必须不可推导：每次开启都重新生成，旧地址不再可用 */
	token, err := random.Token(shareTokenBytes)
	if err != nil {
		return ShareLinkView{}, errs.New(errs.CodeInternal, "生成分享令牌失败").WithCause(err)
	}
	now := s.clock.Now()
	if link == nil {
		link = entity.NewShareLink(userID, noteID, token, now)
	} else {
		link.Reopen(token, now)
	}
	if err := s.deps.Shares.Save(ctx, link); err != nil {
		return ShareLinkView{}, err
	}
	return s.toView(link), nil
}

/*
 * DisableShare 关闭分享，公开地址立即失效。
 * 幂等：从未开启过分享时同样返回成功，契约以笔记存在性决定 404。
 * 参考：docs/API.md#59-分享
 */
func (s *Service) DisableShare(ctx context.Context, userID, noteID string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	if _, err := s.deps.Notes.GetByID(ctx, userID, noteID); err != nil {
		return err
	}
	return s.deps.Shares.Delete(ctx, userID, noteID)
}

/* toView 映射分享状态；开启时拼接公开地址 */
func (s *Service) toView(link *entity.ShareLink) ShareLinkView {
	view := ShareLinkView{
		Enabled:   link.Enabled,
		CreatedAt: link.CreatedAt,
		UpdatedAt: link.UpdatedAt,
	}
	if link.Enabled && link.Token != "" {
		view.URL = s.base + shareURLPrefix + link.Token
	}
	return view
}

/* normalizeToken 去除空白，公开读取时令牌不允许包含空白字符 */
func normalizeToken(raw string) string {
	return strings.TrimSpace(raw)
}
