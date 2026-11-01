/* 标签写入用例：新建、重命名、删除标签与覆盖设置笔记标签 */
package tag

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * CreateTag 新建账号级标签。
 * 约束：名称 1–20 字符且无首尾空格；账号内重名（大小写不敏感）返回 NAME_DUPLICATED。
 * 参考：docs/API.md#57-标签
 */
func (s *Service) CreateTag(ctx context.Context, input CreateTagInput) (TagView, error) {
	if err := requireUser(input.UserID); err != nil {
		return TagView{}, err
	}
	name, err := valueobject.ParseTagName(input.Name)
	if err != nil {
		return TagView{}, err
	}
	if err := s.requireUniqueName(ctx, input.UserID, name, ""); err != nil {
		return TagView{}, err
	}
	id, err := s.newID()
	if err != nil {
		return TagView{}, err
	}
	tag := entity.NewTag(id, input.UserID, name, s.clock.Now())
	if err := s.deps.Tags.Create(ctx, tag); err != nil {
		return TagView{}, err
	}
	return toTagView(tag, 0), nil
}

/*
 * RenameTag 重命名标签。
 * 关联笔记无需级联更新：关联仅保存标签 ID，展示时回查新名称。
 */
func (s *Service) RenameTag(ctx context.Context, input RenameTagInput) (TagView, error) {
	if err := requireUser(input.UserID); err != nil {
		return TagView{}, err
	}
	name, err := valueobject.ParseTagName(input.Name)
	if err != nil {
		return TagView{}, err
	}
	tag, err := s.deps.Tags.GetByID(ctx, input.UserID, input.TagID)
	if err != nil {
		return TagView{}, err
	}
	if err := s.requireUniqueName(ctx, input.UserID, name, tag.ID); err != nil {
		return TagView{}, err
	}
	tag.Rename(name, s.clock.Now())
	if err := s.deps.Tags.Update(ctx, tag); err != nil {
		return TagView{}, err
	}
	counts, err := s.deps.NoteTags.CountsByTags(ctx, input.UserID, []string{tag.ID})
	if err != nil {
		return TagView{}, err
	}
	return toTagView(tag, counts[tag.ID]), nil
}

/*
 * DeleteTag 删除标签并解除全部关联，不删除笔记。
 * 关联解除与标签删除在同一事务内完成，避免出现悬空关联。
 * 参考：docs/API.md#57-标签、PRD FR-13
 */
func (s *Service) DeleteTag(ctx context.Context, userID, tagID string) error {
	if err := requireUser(userID); err != nil {
		return err
	}
	if _, err := s.deps.Tags.GetByID(ctx, userID, tagID); err != nil {
		return err
	}
	return s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := s.deps.NoteTags.DeleteByTag(txCtx, userID, tagID); err != nil {
			return err
		}
		return s.deps.Tags.Delete(txCtx, userID, tagID)
	})
}

/*
 * SetNoteTags 整体覆盖一篇笔记的标签。
 * 约束：单篇最多 10 个（超出 409 TAG_LIMIT_EXCEEDED）；标签不存在或不属于当前用户返回 404；
 * 回收站笔记不可设置标签（409 NOTE_STATE_INVALID）。
 * 参考：docs/API.md#57-标签、PRD FR-13
 */
func (s *Service) SetNoteTags(ctx context.Context, input SetNoteTagsInput) (SetNoteTagsResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return SetNoteTagsResult{}, err
	}
	note, err := s.deps.Notes.GetByID(ctx, input.UserID, input.NoteID)
	if err != nil {
		return SetNoteTagsResult{}, err
	}
	if note.IsDeleted() {
		return SetNoteTagsResult{}, errs.New(errs.CodeNoteStateInvalid, "回收站笔记不可设置标签")
	}
	tagIDs := dedupeIDs(input.TagIDs)
	if len(tagIDs) > MaxTagsPerNote {
		return SetNoteTagsResult{}, errs.New(errs.CodeTagLimitExceeded,
			"单篇笔记最多 10 个标签").WithMeta("limit", MaxTagsPerNote)
	}

	tags := make([]*entity.Tag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		tag, err := s.deps.Tags.GetByID(ctx, input.UserID, tagID)
		if err != nil {
			return SetNoteTagsResult{}, err
		}
		tags = append(tags, tag)
	}

	if err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		return s.deps.NoteTags.ReplaceForNote(txCtx, input.UserID, input.NoteID, tagIDs)
	}); err != nil {
		return SetNoteTagsResult{}, err
	}

	counts, err := s.deps.NoteTags.CountsByTags(ctx, input.UserID, tagIDs)
	if err != nil {
		return SetNoteTagsResult{}, err
	}
	views := make([]TagView, 0, len(tags))
	for _, tag := range tags {
		views = append(views, toTagView(tag, counts[tag.ID]))
	}
	return SetNoteTagsResult{Items: views}, nil
}

/* requireUniqueName 扫描账号内标签名，excludeID 用于重命名时排除自身 */
func (s *Service) requireUniqueName(ctx context.Context, userID string, name valueobject.TagName, excludeID string) error {
	tags, err := s.deps.Tags.List(ctx, userID)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if tag.ID == excludeID {
			continue
		}
		if strings.EqualFold(tag.Name, name.String()) {
			return errs.New(errs.CodeNameDuplicated, "账号内已存在同名标签")
		}
	}
	return nil
}
