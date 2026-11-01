/* 笔记写入用例：新建、乐观锁保存（含冲突副本）与移动 */
package note

import (
	"context"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* saveSourceManual/Auto 是 PATCH 允许的来源枚举；当前阶段只做取值校验 */
const (
	saveSourceManual = "MANUAL_SAVE"
	saveSourceAuto   = "AUTO_SAVE"
)

/*
 * CreateNote 在指定目录新建空白笔记。
 * 约束：目录必须可见；clientCreatedAt 只用于离线补录排序，未来时间按当前时间收敛。
 * 参考：docs/API.md#533-post-v1notes
 */
func (s *Service) CreateNote(ctx context.Context, input CreateNoteInput) (NoteView, error) {
	if err := requireUser(input.UserID); err != nil {
		return NoteView{}, err
	}
	if input.FolderID == "" {
		return NoteView{}, errs.New(errs.CodeValidationFailed, "folderId 必填")
	}
	title, err := valueobject.ParseNoteTitle(input.Title)
	if err != nil {
		return NoteView{}, err
	}
	content, err := valueobject.ParseNoteContent(input.Content)
	if err != nil {
		return NoteView{}, err
	}

	now := s.clock.Now()
	createdAt, err := clientCreatedAt(input.ClientCreatedAt, now)
	if err != nil {
		return NoteView{}, err
	}

	var created *entity.Note
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		if _, err := s.activeFolder(txCtx, input.UserID, input.FolderID); err != nil {
			return err
		}
		id, err := s.newID("笔记")
		if err != nil {
			return err
		}
		note := entity.NewNote(id, input.UserID, input.FolderID, title, content, createdAt, now)
		if err := s.deps.Notes.Create(txCtx, note); err != nil {
			return err
		}
		created = note
		return nil
	})
	if err != nil {
		return NoteView{}, err
	}
	return s.buildView(ctx, input.UserID, created)
}

/*
 * UpdateNote 按修订号保存标题与正文。
 * 裁决：修订号不匹配时先把当前正文另存为冲突副本，再写入本次内容，**不返回 412**。
 * 参考：docs/API.md#536-patch-v1notesnoteid、docs/ARCHITECTURE.md#63-写冲突与离线编辑
 */
func (s *Service) UpdateNote(ctx context.Context, input UpdateNoteInput) (SaveNoteResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return SaveNoteResult{}, err
	}
	if input.Revision <= 0 {
		return SaveNoteResult{}, errs.New(errs.CodeValidationFailed, "revision 必填且必须为正整数")
	}
	if err := validateSaveSource(input.SaveSource); err != nil {
		return SaveNoteResult{}, err
	}
	var title valueobject.NoteTitle
	if input.Title != nil {
		parsed, err := valueobject.ParseNoteTitle(*input.Title)
		if err != nil {
			return SaveNoteResult{}, err
		}
		title = parsed
	}
	var content valueobject.NoteContent
	if input.Content != nil {
		parsed, err := valueobject.ParseNoteContent(*input.Content)
		if err != nil {
			return SaveNoteResult{}, err
		}
		content = parsed
	}

	now := s.clock.Now()
	var result SaveNoteResult
	var saved *entity.Note
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		/* 加行锁读取：并发保存必须串行化，否则后写者的 revision 校验会基于过期快照 */
		current, err := s.deps.Notes.GetForUpdate(txCtx, input.UserID, input.NoteID)
		if err != nil {
			return err
		}
		if err := current.Editable(); err != nil {
			return err
		}
		/* 写入前快照修订号，供 UPDATE 的 revision = expected 条件使用 */
		expected := current.Revision

		/* 修订号落后：当前正文另存为冲突副本，副本标题带时间后缀 */
		if current.Revision != input.Revision {
			copyID, err := s.newID("冲突副本")
			if err != nil {
				return err
			}
			base := valueobject.NoteTitle(current.DisplayTitle())
			copied := entity.NewConflictCopy(copyID, input.UserID, current.FolderID,
				base.WithConflictSuffix(now), valueobject.NoteContent(current.Content), now)
			if err := s.deps.Notes.Create(txCtx, copied); err != nil {
				return err
			}
			/* 覆盖前的正文同时留档为 CONFLICT_COPY 版本，便于事后回看（PRD FR-16） */
			if err := s.deps.Versions.Record(txCtx, input.UserID, current.ID, "CONFLICT_COPY", current.Content); err != nil {
				return err
			}
			result.ConflictCopyID = copyID
		}

		nextTitle := valueobject.NoteTitle(current.Title)
		if input.Title != nil {
			nextTitle = title
		}
		nextContent := valueobject.NoteContent(current.Content)
		if input.Content != nil {
			nextContent = content
		}
		if err := current.Save(nextTitle, nextContent, now); err != nil {
			return err
		}
		/* 条件更新：0 行命中说明并发写已发生，必须失败而不是静默覆盖 */
		applied, err := s.deps.Notes.UpdateWithRevision(txCtx, current, expected)
		if err != nil {
			return err
		}
		if !applied {
			return errs.New(errs.CodeInternal, "笔记版本并发冲突，请重试")
		}
		/* 每次保存落一条版本；自动保存按 5 分钟节流由版本用例判定（PRD FR-16） */
		versionSource := saveSourceManual
		if strings.TrimSpace(input.SaveSource) == saveSourceAuto {
			versionSource = "AUTO_SAVE"
		}
		if err := s.deps.Versions.Record(txCtx, input.UserID, current.ID, versionSource, current.Content); err != nil {
			return err
		}
		saved = current
		return nil
	})
	if err != nil {
		return SaveNoteResult{}, err
	}
	view, err := s.buildView(ctx, input.UserID, saved)
	if err != nil {
		return SaveNoteResult{}, err
	}
	result.Note = view
	return result, nil
}

/*
 * MoveNote 把笔记移动到目标目录。
 * 约束：笔记必须可编辑，目标目录必须可见；移动不改变修订号。
 * 参考：docs/API.md#538-post-v1notesnoteidmove
 */
func (s *Service) MoveNote(ctx context.Context, input MoveNoteInput) (NoteView, error) {
	if err := requireUser(input.UserID); err != nil {
		return NoteView{}, err
	}
	if input.FolderID == "" {
		return NoteView{}, errs.New(errs.CodeValidationFailed, "folderId 必填")
	}
	now := s.clock.Now()
	var moved *entity.Note
	err := s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		note, err := s.deps.Notes.GetForUpdate(txCtx, input.UserID, input.NoteID)
		if err != nil {
			return err
		}
		if err := note.Editable(); err != nil {
			return err
		}
		if _, err := s.activeFolder(txCtx, input.UserID, input.FolderID); err != nil {
			return err
		}
		if err := note.Move(input.FolderID, now); err != nil {
			return err
		}
		if err := s.deps.Notes.Update(txCtx, note); err != nil {
			return err
		}
		moved = note
		return nil
	})
	if err != nil {
		return NoteView{}, err
	}
	return s.buildView(ctx, input.UserID, moved)
}

/* validateSaveSource 只接受缺省、MANUAL_SAVE 与 AUTO_SAVE */
func validateSaveSource(raw string) error {
	switch strings.TrimSpace(raw) {
	case "", saveSourceManual, saveSourceAuto:
		return nil
	default:
		return errs.New(errs.CodeValidationFailed, "saveSource 只支持 MANUAL_SAVE 或 AUTO_SAVE")
	}
}

/* clientCreatedAt 解析离线创建时间；缺省或未来时间回落到服务端当前时间 */
func clientCreatedAt(raw string, now time.Time) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return now, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, errs.New(errs.CodeValidationFailed,
			"clientCreatedAt 必须为 RFC 3339 时间").WithCause(err)
	}
	if parsed.After(now) {
		return now, nil
	}
	return parsed, nil
}
