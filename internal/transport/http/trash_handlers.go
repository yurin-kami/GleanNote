/* 回收站端点：按删除批次聚合目录与笔记的只读视图 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	trashusecase "github.com/gleannote/gleannote/internal/application/usecase/trash"
)

/* trashEntryDTO 与 openapi 的 TrashEntry 对齐 */
type trashEntryDTO struct {
	BatchID       string `json:"batchId"`
	ItemType      string `json:"itemType"`
	ItemID        string `json:"itemId"`
	Name          string `json:"name"`
	OriginalPath  string `json:"originalPath,omitempty"`
	FolderCount   int    `json:"folderCount"`
	NoteCount     int    `json:"noteCount"`
	DeletedAt     string `json:"deletedAt"`
	PurgeAt       string `json:"purgeAt"`
	RemainingDays int    `json:"remainingDays"`
}

/* trashListDTO 是回收站集合响应；契约未定义 totalSize */
type trashListDTO struct {
	Items         []trashEntryDTO `json:"items"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

/* toTrashEntryDTO 映射批次聚合视图；时间统一 UTC + RFC3339 */
func toTrashEntryDTO(view trashusecase.TrashItemView) trashEntryDTO {
	return trashEntryDTO{
		BatchID:       view.BatchID,
		ItemType:      view.ItemType,
		ItemID:        view.ItemID,
		Name:          view.Name,
		OriginalPath:  view.OriginalPath,
		FolderCount:   view.FolderCount,
		NoteCount:     view.NoteCount,
		DeletedAt:     rfc3339(view.DeletedAt),
		PurgeAt:       rfc3339(view.PurgeAt),
		RemainingDays: view.RemainingDays,
	}
}

/* listTrash 处理 GET /v1/trash；回收站本身不提供写操作 */
func (h *handler) listTrash(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	pageSize, err := parsePageSizeQuery(c)
	if err != nil {
		respondError(c, err)
		return
	}
	result, err := h.trash.ListTrash(c.Request.Context(), trashusecase.ListTrashInput{
		UserID:    userID,
		PageSize:  pageSize,
		PageToken: c.Query("pageToken"),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]trashEntryDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toTrashEntryDTO(item))
	}
	respondJSON(c, http.StatusOK, trashListDTO{Items: items, NextPageToken: result.NextPageToken})
}
