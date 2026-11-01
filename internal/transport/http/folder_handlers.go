/* 目录端点：列表、创建、详情、面包屑、改名、移动、删除、恢复与彻底删除 */
package httptransport

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	folderusecase "github.com/gleannote/gleannote/internal/application/usecase/folder"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* folderDTO 与 openapi 的 Folder 对齐；noteCount 为目录内未删除笔记数 */
type folderDTO struct {
	ID             string  `json:"id"`
	ParentID       *string `json:"parentId"`
	Name           string  `json:"name"`
	Depth          int     `json:"depth"`
	Revision       int     `json:"revision"`
	ChildCount     int     `json:"childCount,omitempty"`
	NoteCount      int     `json:"noteCount,omitempty"`
	RestoredToRoot *bool   `json:"restoredToRoot,omitempty"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

/* folderListDTO 是 AIP-158 集合响应 */
type folderListDTO struct {
	Items         []folderDTO `json:"items"`
	NextPageToken string      `json:"nextPageToken,omitempty"`
	TotalSize     int         `json:"totalSize"`
}

/* folderPathItemDTO 是面包屑的一段 */
type folderPathItemDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

/* folderPathDTO 是面包屑与原路径展示 */
type folderPathDTO struct {
	Items       []folderPathItemDTO `json:"items"`
	DisplayPath string              `json:"displayPath"`
}

/* moveResultDTO 汇总移动结果；movedNoteCount 为子树内未删除笔记数 */
type moveResultDTO struct {
	Folder           folderDTO `json:"folder"`
	MovedFolderCount int       `json:"movedFolderCount"`
	MovedNoteCount   int       `json:"movedNoteCount,omitempty"`
}

/* deleteResultDTO 是软删除后的回收站记录摘要 */
type deleteResultDTO struct {
	DeleteBatchID string `json:"deleteBatchId"`
	FolderCount   int    `json:"folderCount"`
	NoteCount     int    `json:"noteCount,omitempty"`
	PurgeAt       string `json:"purgeAt"`
}

/* toFolderDTO 映射目录视图；restoredToRoot 仅恢复接口传入 */
func toFolderDTO(view folderusecase.FolderView, restoredToRoot *bool) folderDTO {
	return folderDTO{
		ID:             view.ID,
		ParentID:       view.ParentID,
		Name:           view.Name,
		Depth:          view.Depth,
		Revision:       view.Revision,
		ChildCount:     view.ChildCount,
		NoteCount:      view.NoteCount,
		RestoredToRoot: restoredToRoot,
		CreatedAt:      rfc3339(view.CreatedAt),
		UpdatedAt:      rfc3339(view.UpdatedAt),
	}
}

/* listFolders 处理 GET /v1/folders；pageSize 非法取值按 400 处理，超过上限由用例收敛 */
func (h *handler) listFolders(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	pageSize := 0
	if raw := c.Query("pageSize"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			respondError(c, errs.New(errs.CodeValidationFailed, "pageSize 必须为非负整数"))
			return
		}
		pageSize = parsed
	}
	result, err := h.folders.ListFolders(c.Request.Context(), folderusecase.ListFoldersInput{
		UserID:    userID,
		ParentID:  c.Query("parentId"),
		PageToken: c.Query("pageToken"),
		OrderBy:   c.Query("orderBy"),
		PageSize:  pageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]folderDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toFolderDTO(item, nil))
	}
	respondJSON(c, http.StatusOK, folderListDTO{
		Items:         items,
		NextPageToken: result.NextPageToken,
		TotalSize:     result.TotalSize,
	})
}

/* createFolderRequest 对应 POST /v1/folders；parentId 缺省表示根级 */
type createFolderRequest struct {
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

/* createFolder 新建目录并返回 201 */
func (h *handler) createFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createFolderRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.folders.CreateFolder(c.Request.Context(), folderusecase.CreateFolderInput{
		UserID:   userID,
		ParentID: payload.ParentID,
		Name:     payload.Name,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, toFolderDTO(view, nil))
}

/* getFolder 处理 GET /v1/folders/{folderId} */
func (h *handler) getFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.folders.GetFolder(c.Request.Context(), userID, c.Param("folderId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toFolderDTO(view, nil))
}

/* folderPath 处理 GET /v1/folders/{folderId}/path */
func (h *handler) folderPath(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.folders.FolderPath(c.Request.Context(), userID, c.Param("folderId"))
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]folderPathItemDTO, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, folderPathItemDTO{ID: item.ID, Name: item.Name})
	}
	respondJSON(c, http.StatusOK, folderPathDTO{Items: items, DisplayPath: view.DisplayPath})
}

/* renameFolderRequest 对应 PATCH /v1/folders/{folderId} */
type renameFolderRequest struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

/* renameFolder 按修订号乐观锁改名 */
func (h *handler) renameFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload renameFolderRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.folders.RenameFolder(c.Request.Context(), folderusecase.RenameFolderInput{
		UserID:   userID,
		FolderID: c.Param("folderId"),
		Name:     payload.Name,
		Revision: payload.Revision,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toFolderDTO(view, nil))
}

/* deleteFolder 软删除整棵子树并返回回收站摘要 */
func (h *handler) deleteFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	result, err := h.folders.DeleteFolder(c.Request.Context(), userID, c.Param("folderId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, deleteResultDTO{
		DeleteBatchID: result.DeleteBatchID,
		FolderCount:   result.FolderCount,
		NoteCount:     result.NoteCount,
		PurgeAt:       rfc3339(result.PurgeAt),
	})
}

/* moveFolderRequest 对应 POST /v1/folders/{folderId}:move；null 表示移到根级 */
type moveFolderRequest struct {
	TargetParentID *string `json:"targetParentId"`
}

/* moveFolder 移动目录及其子树 */
func (h *handler) moveFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload moveFolderRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	target := ""
	if payload.TargetParentID != nil {
		target = *payload.TargetParentID
	}
	result, err := h.folders.MoveFolder(c.Request.Context(), folderusecase.MoveFolderInput{
		UserID:         userID,
		FolderID:       c.Param("folderId"),
		TargetParentID: target,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, moveResultDTO{
		Folder:           toFolderDTO(result.Folder, nil),
		MovedFolderCount: result.MovedFolderCount,
		MovedNoteCount:   result.MovedNoteCount,
	})
}

/* undeleteFolder 恢复回收站中的目录，并显式标注是否恢复到根级 */
func (h *handler) undeleteFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.folders.RestoreFolder(c.Request.Context(), userID, c.Param("folderId"))
	if err != nil {
		respondError(c, err)
		return
	}
	restoredToRoot := view.RestoredToRoot
	respondJSON(c, http.StatusOK, toFolderDTO(view, &restoredToRoot))
}

/* expungeFolder 彻底删除回收站中的目录，成功返回 204 */
func (h *handler) expungeFolder(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.folders.ExpungeFolder(c.Request.Context(), userID, c.Param("folderId")); err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}
