/* 标签端点：列表、新建、重命名、删除与覆盖设置笔记标签 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	tagusecase "github.com/gleannote/gleannote/internal/application/usecase/tag"
)

/* tagViewDTO 与 openapi 的 Tag 对齐；列表与写入响应共用 */
type tagViewDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NoteCount int    `json:"noteCount"`
	CreatedAt string `json:"createdAt"`
}

/* tagListDTO 是标签集合响应；契约未定义 totalSize */
type tagListDTO struct {
	Items         []tagViewDTO `json:"items"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}

/* setNoteTagsDTO 是覆盖设置笔记标签后的响应体 */
type setNoteTagsDTO struct {
	Items []tagViewDTO `json:"items"`
}

/* createTagRequest 对应 POST /v1/tags 与 PATCH /v1/tags/{tagId} */
type createTagRequest struct {
	Name string `json:"name"`
}

/* setNoteTagsRequest 对应 PUT /v1/notes/{noteId}/tags，语义为整体覆盖 */
type setNoteTagsRequest struct {
	TagIDs []string `json:"tagIds"`
}

/* toTagViewDTO 映射标签视图；createdAt 统一 UTC + RFC3339 */
func toTagViewDTO(view tagusecase.TagView) tagViewDTO {
	return tagViewDTO{
		ID:        view.ID,
		Name:      view.Name,
		NoteCount: view.NoteCount,
		CreatedAt: rfc3339(view.CreatedAt),
	}
}

/* listTags 处理 GET /v1/tags；orderBy 缺省为笔记数倒序 */
func (h *handler) listTags(c *gin.Context) {
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
	result, err := h.tags.ListTags(c.Request.Context(), tagusecase.ListTagsInput{
		UserID:    userID,
		OrderBy:   c.Query("orderBy"),
		PageToken: c.Query("pageToken"),
		PageSize:  pageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]tagViewDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toTagViewDTO(item))
	}
	respondJSON(c, http.StatusOK, tagListDTO{Items: items, NextPageToken: result.NextPageToken})
}

/* createTag 新建账号级标签并返回 201 */
func (h *handler) createTag(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createTagRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.tags.CreateTag(c.Request.Context(), tagusecase.CreateTagInput{
		UserID: userID,
		Name:   payload.Name,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, toTagViewDTO(view))
}

/* renameTag 重命名标签；关联笔记读取时自动展示新名称 */
func (h *handler) renameTag(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createTagRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.tags.RenameTag(c.Request.Context(), tagusecase.RenameTagInput{
		UserID: userID,
		TagID:  c.Param("tagId"),
		Name:   payload.Name,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toTagViewDTO(view))
}

/* deleteTag 删除标签并解除全部关联，成功返回 204 */
func (h *handler) deleteTag(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.tags.DeleteTag(c.Request.Context(), userID, c.Param("tagId")); err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}

/* setNoteTags 整体覆盖一篇笔记的标签，返回覆盖后的标签列表 */
func (h *handler) setNoteTags(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload setNoteTagsRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	result, err := h.tags.SetNoteTags(c.Request.Context(), tagusecase.SetNoteTagsInput{
		UserID: userID,
		NoteID: c.Param("noteId"),
		TagIDs: payload.TagIDs,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]tagViewDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toTagViewDTO(item))
	}
	respondJSON(c, http.StatusOK, setNoteTagsDTO{Items: items})
}
