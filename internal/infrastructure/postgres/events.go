/* 变更事件构造：与内存实现保持同一投影口径，保证两种存储下同步语义一致 */
package postgres

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* noteChangeEvent 构造笔记事件；DELETE 的载荷由日志仓储统一清空 */
func noteChangeEvent(note *entity.Note, op entity.ChangeOperation, now time.Time) *entity.ChangeEvent {
	event := &entity.ChangeEvent{
		UserID:       note.UserID,
		ResourceType: entity.ChangeResourceNote,
		ResourceID:   note.ID,
		Op:           op,
		Revision:     note.Revision,
		UpdatedAt:    now,
	}
	if op != entity.ChangeOpDelete {
		event.Payload = map[string]any{
			"title":    note.Title,
			"content":  note.Content,
			"folderId": note.FolderID,
			"state":    string(note.State),
		}
	}
	return event
}

/* folderChangeEvent 构造目录事件；载荷保留层级信息供客户端重建树 */
func folderChangeEvent(folder *entity.Folder, op entity.ChangeOperation, now time.Time) *entity.ChangeEvent {
	event := &entity.ChangeEvent{
		UserID:       folder.UserID,
		ResourceType: entity.ChangeResourceFolder,
		ResourceID:   folder.ID,
		Op:           op,
		Revision:     folder.Revision,
		UpdatedAt:    now,
	}
	if op != entity.ChangeOpDelete {
		event.Payload = map[string]any{
			"name":     folder.Name,
			"depth":    folder.Depth,
			"path":     folder.Path,
			"parentId": folder.ParentID,
		}
	}
	return event
}

/* tagChangeEvent 构造标签事件；载荷只保留名称 */
func tagChangeEvent(tag *entity.Tag, op entity.ChangeOperation, now time.Time) *entity.ChangeEvent {
	event := &entity.ChangeEvent{
		UserID:       tag.UserID,
		ResourceType: entity.ChangeResourceTag,
		ResourceID:   tag.ID,
		Op:           op,
		UpdatedAt:    now,
	}
	if op != entity.ChangeOpDelete {
		event.Payload = map[string]any{"name": tag.Name}
	}
	return event
}

/* noteTagsChangeEvent 构造笔记标签变更事件；客户端据此合并标签关联 */
func noteTagsChangeEvent(userID, noteID string, tagIDs []string, now time.Time) *entity.ChangeEvent {
	return &entity.ChangeEvent{
		UserID:       userID,
		ResourceType: entity.ChangeResourceNote,
		ResourceID:   noteID,
		Op:           entity.ChangeOpUpdate,
		UpdatedAt:    now,
		Payload:      map[string]any{"tagIds": append([]string(nil), tagIDs...)},
	}
}

/* pageChangeEvent 构造页面新增事件；墓碑由笔记删除事件覆盖 */
func pageChangeEvent(page *entity.NotePage, now time.Time) *entity.ChangeEvent {
	return &entity.ChangeEvent{
		UserID:       page.UserID,
		ResourceType: entity.ChangeResourcePage,
		ResourceID:   page.ID,
		Op:           entity.ChangeOpCreate,
		UpdatedAt:    now,
		Payload: map[string]any{
			"noteId": page.NoteID,
			"index":  page.Index,
			"bytes":  page.Bytes,
			"width":  page.Width,
			"height": page.Height,
		},
	}
}
