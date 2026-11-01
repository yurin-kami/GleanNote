/* 导出与批量导入端点：任务发起与结果查询 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	exportusecase "github.com/gleannote/gleannote/internal/application/usecase/export"
	importsusecase "github.com/gleannote/gleannote/internal/application/usecase/imports"
)

/* exportDTO 与 openapi 的 Export 对齐；成功态才输出下载地址 */
type exportDTO struct {
	ExportID    string `json:"exportId"`
	State       string `json:"state"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	FailureCode string `json:"failureCode,omitempty"`
}

/* createExportRequest 对应 POST /v1/exports */
type createExportRequest struct {
	NoteID   string `json:"noteId"`
	FolderID string `json:"folderId"`
	Format   string `json:"format"`
}

/* toExportDTO 映射导出任务视图，时间统一为 UTC RFC3339 */
func toExportDTO(view exportusecase.ExportView) exportDTO {
	dto := exportDTO{
		ExportID:    view.ID,
		State:       view.State,
		DownloadURL: view.DownloadURL,
		FailureCode: view.FailureCode,
	}
	if view.ExpiresAt != nil {
		dto.ExpiresAt = rfc3339(*view.ExpiresAt)
	}
	return dto
}

/* createExport 发起导出；当前阶段同步生成，202 响应可能已是终态 */
func (h *handler) createExport(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createExportRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.exports.CreateExport(c.Request.Context(), exportusecase.CreateExportInput{
		UserID:   userID,
		NoteID:   payload.NoteID,
		FolderID: payload.FolderID,
		Format:   payload.Format,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusAccepted, toExportDTO(view))
}

/* getExport 查询导出状态，成功态返回新的下载地址 */
func (h *handler) getExport(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.exports.GetExport(c.Request.Context(), userID, c.Param("exportId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toExportDTO(view))
}

/* importItemDTO 与 openapi 的 Import.items 对齐 */
type importItemDTO struct {
	UploadID    string `json:"uploadId"`
	NoteID      string `json:"noteId,omitempty"`
	State       string `json:"state"`
	FailureCode string `json:"failureCode,omitempty"`
}

/* importDTO 与 openapi 的 Import 对齐 */
type importDTO struct {
	ImportID  string          `json:"importId"`
	State     string          `json:"state"`
	Total     int             `json:"total"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Items     []importItemDTO `json:"items"`
}

/* createImportRequest 对应 POST /v1/imports */
type createImportRequest struct {
	FolderID  string   `json:"folderId"`
	Mode      string   `json:"mode"`
	UploadIDs []string `json:"uploadIds"`
}

/* toImportDTO 映射导入任务视图 */
func toImportDTO(view importsusecase.ImportView) importDTO {
	items := make([]importItemDTO, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, importItemDTO{
			UploadID:    item.UploadID,
			NoteID:      item.NoteID,
			State:       item.State,
			FailureCode: item.FailureCode,
		})
	}
	return importDTO{
		ImportID:  view.ID,
		State:     view.State,
		Total:     view.Total,
		Succeeded: view.Succeeded,
		Failed:    view.Failed,
		Items:     items,
	}
}

/* createImport 发起批量导入；当前阶段串行处理并同步返回终态 */
func (h *handler) createImport(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createImportRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.imports.CreateImport(c.Request.Context(), importsusecase.CreateImportInput{
		UserID:    userID,
		FolderID:  payload.FolderID,
		Mode:      payload.Mode,
		UploadIDs: payload.UploadIDs,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusAccepted, toImportDTO(view))
}

/* getImport 查询导入进度与逐项结果 */
func (h *handler) getImport(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.imports.GetImport(c.Request.Context(), userID, c.Param("importId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toImportDTO(view))
}
