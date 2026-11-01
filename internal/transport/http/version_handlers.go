/* 版本端点：列表、正文与恢复 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	versionusecase "github.com/gleannote/gleannote/internal/application/usecase/version"
)

/* versionDTO 对应 openapi 的 Version；正文只在详情中出现 */
type versionDTO struct {
	VersionID string `json:"versionId"`
	Source    string `json:"source"`
	CharCount int    `json:"charCount"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

/* versionDetailDTO 在列表项基础上补充正文 */
type versionDetailDTO struct {
	versionDTO
	Content string `json:"content"`
}

/* versionListDTO 是 AIP-158 集合响应 */
type versionListDTO struct {
	Items         []versionDTO `json:"items"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}

/* toVersionDTO 映射版本列表项 */
func toVersionDTO(view versionusecase.VersionView) versionDTO {
	return versionDTO{
		VersionID: view.ID,
		Source:    view.Source,
		CharCount: view.CharCount,
		CreatedAt: rfc3339(view.CreatedAt),
		ExpiresAt: rfc3339(view.ExpiresAt),
	}
}

/* listVersions 处理 GET /v1/notes/{noteId}/versions */
func (h *handler) listVersions(c *gin.Context) {
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
	result, err := h.versions.ListVersions(c.Request.Context(), userID, c.Param("noteId"),
		pageSize, c.Query("pageToken"))
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]versionDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toVersionDTO(item))
	}
	respondJSON(c, http.StatusOK, versionListDTO{Items: items, NextPageToken: result.NextPageToken})
}

/* getVersion 处理 GET /v1/notes/{noteId}/versions/{versionId} */
func (h *handler) getVersion(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.versions.GetVersion(c.Request.Context(), userID, c.Param("noteId"), c.Param("versionId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, versionDetailDTO{versionDTO: toVersionDTO(view.VersionView), Content: view.Content})
}

/*
 * restoreVersion 处理 POST /v1/notes/{noteId}/versions/{versionId}:restore。
 * 只替换正文并生成 RESTORE 版本；响应为恢复后的笔记详情。
 */
func (h *handler) restoreVersion(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	noteID, err := h.versions.RestoreVersion(c.Request.Context(), userID,
		c.Param("noteId"), c.Param("versionId"))
	if err != nil {
		respondError(c, err)
		return
	}
	view, err := h.notes.GetNote(c.Request.Context(), userID, noteID)
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toNoteDetailDTO(view))
}
