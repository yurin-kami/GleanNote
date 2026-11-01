/* 图片上传、笔记页面与转换任务端点；另含由图片创建笔记的自定义方法 */
package httptransport

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	conversionusecase "github.com/gleannote/gleannote/internal/application/usecase/conversion"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	uploadusecase "github.com/gleannote/gleannote/internal/application/usecase/upload"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* uploadSessionDTO 对应 openapi 的 UploadSession；headers 必须由客户端原样携带 */
type uploadSessionDTO struct {
	UploadID  string            `json:"uploadId"`
	ObjectKey string            `json:"objectKey"`
	PutURL    string            `json:"putUrl"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expiresAt"`
}

/* uploadSessionsDTO 是批量申请上传会话的响应体 */
type uploadSessionsDTO struct {
	Items []uploadSessionDTO `json:"items"`
}

/* uploadedPageDTO 对应 openapi 的 UploadedPage；缩略图地址 10 分钟有效 */
type uploadedPageDTO struct {
	UploadID string `json:"uploadId"`
	Status   string `json:"status"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Bytes    int64  `json:"bytes"`
	ThumbURL string `json:"thumbUrl,omitempty"`
}

/* pageDTO 对应 openapi 的 Page，只输出缩略图地址 */
type pageDTO struct {
	ID        string `json:"id"`
	Index     int    `json:"index"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Bytes     int64  `json:"bytes"`
	ThumbURL  string `json:"thumbUrl,omitempty"`
	CreatedAt string `json:"createdAt"`
}

/* pageListDTO 是页面集合响应；nextPageToken 为空表示末页 */
type pageListDTO struct {
	Items         []pageDTO `json:"items"`
	NextPageToken string    `json:"nextPageToken,omitempty"`
}

/* pageOriginalDTO 对应 openapi 的 PageOriginal */
type pageOriginalDTO struct {
	ID          string `json:"id"`
	Index       int    `json:"index"`
	OriginalURL string `json:"originalUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

/* conversionJobDTO 对应 openapi 的 ConversionJob；失败字段非失败态时省略 */
type conversionJobDTO struct {
	ID                 string `json:"id"`
	NoteID             string `json:"noteId"`
	Kind               string `json:"kind"`
	State              string `json:"state"`
	Phase              string `json:"phase"`
	TotalPages         int    `json:"totalPages"`
	ProcessedPages     int    `json:"processedPages"`
	FailureCode        string `json:"failureCode,omitempty"`
	FailureUserMessage string `json:"failureUserMessage,omitempty"`
	QuotaCharged       int    `json:"quotaCharged"`
	CreatedAt          string `json:"createdAt"`
	StartedAt          string `json:"startedAt,omitempty"`
	FinishedAt         string `json:"finishedAt,omitempty"`
}

/* quotaSnapshotDTO 是提交响应中的额度快照 */
type quotaSnapshotDTO struct {
	Remaining int `json:"remaining"`
	Charged   int `json:"charged"`
}

/* createFromPagesResponseDTO 返回草稿、任务与额度 */
type createFromPagesResponseDTO struct {
	Note          noteSummaryDTO   `json:"note"`
	ConversionJob conversionJobDTO `json:"conversionJob"`
	Quota         quotaSnapshotDTO `json:"quota"`
}

/* toConversionJobDTO 映射任务视图，时间统一为 UTC RFC3339 */
func toConversionJobDTO(view conversionusecase.JobView) conversionJobDTO {
	dto := conversionJobDTO{
		ID:                 view.ID,
		NoteID:             view.NoteID,
		Kind:               view.Kind,
		State:              view.State,
		Phase:              view.Phase,
		TotalPages:         view.TotalPages,
		ProcessedPages:     view.ProcessedPages,
		FailureCode:        view.FailureCode,
		FailureUserMessage: view.FailureUserMessage,
		QuotaCharged:       view.QuotaCharged,
		CreatedAt:          rfc3339(view.CreatedAt),
	}
	if view.StartedAt != nil {
		dto.StartedAt = rfc3339(*view.StartedAt)
	}
	if view.FinishedAt != nil {
		dto.FinishedAt = rfc3339(*view.FinishedAt)
	}
	return dto
}

/* toSubmittedJobDTO 映射创建笔记响应中的任务摘要 */
func toSubmittedJobDTO(view noteusecase.ConversionJobView) conversionJobDTO {
	return conversionJobDTO{
		ID:             view.ID,
		State:          view.State,
		Phase:          view.Phase,
		TotalPages:     view.TotalPages,
		ProcessedPages: view.ProcessedPages,
	}
}

/* createUploadsRequest 对应 POST /v1/uploads */
type createUploadsRequest struct {
	Items []struct {
		FileName    string `json:"fileName"`
		ContentType string `json:"contentType"`
		Bytes       int64  `json:"bytes"`
		SHA256      string `json:"sha256"`
	} `json:"items"`
}

/* createUploads 批量申请上传会话与预签名地址 */
func (h *handler) createUploads(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createUploadsRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	items := make([]uploadusecase.UploadItem, 0, len(payload.Items))
	for _, item := range payload.Items {
		items = append(items, uploadusecase.UploadItem{
			FileName:    item.FileName,
			ContentType: item.ContentType,
			Bytes:       item.Bytes,
			SHA256:      item.SHA256,
		})
	}
	tickets, err := h.uploads.CreateUploads(c.Request.Context(), userID, items)
	if err != nil {
		respondError(c, err)
		return
	}
	dtos := make([]uploadSessionDTO, 0, len(tickets))
	for _, ticket := range tickets {
		dtos = append(dtos, uploadSessionDTO{
			UploadID:  ticket.UploadID,
			ObjectKey: ticket.ObjectKey,
			PutURL:    ticket.PutURL,
			Headers:   ticket.Headers,
			ExpiresAt: rfc3339(ticket.ExpiresAt),
		})
	}
	respondJSON(c, http.StatusCreated, uploadSessionsDTO{Items: dtos})
}

/* completeUploadRequest 对应 POST /v1/uploads/{uploadId}:complete */
type completeUploadRequest struct {
	SHA256 string `json:"sha256"`
}

/* completeUpload 确认对象已上传并完成校验，生成缩略图 */
func (h *handler) completeUpload(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload completeUploadRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	result, err := h.uploads.CompleteUpload(c.Request.Context(), userID,
		c.Param("uploadId"), payload.SHA256)
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, uploadedPageDTO{
		UploadID: result.UploadID,
		Status:   result.Status,
		Width:    result.Width,
		Height:   result.Height,
		Bytes:    result.Bytes,
		ThumbURL: result.ThumbURL,
	})
}

/* listPages 返回笔记页面缩略图集合；pageSize 负数返回 400 */
func (h *handler) listPages(c *gin.Context) {
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
	result, err := h.uploads.ListPages(c.Request.Context(), userID,
		c.Param("noteId"), pageSize, c.Query("pageToken"))
	if err != nil {
		respondError(c, err)
		return
	}
	items := make([]pageDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, pageDTO{
			ID:        item.ID,
			Index:     item.Index,
			Width:     item.Width,
			Height:    item.Height,
			Bytes:     item.Bytes,
			ThumbURL:  item.ThumbURL,
			CreatedAt: rfc3339(item.CreatedAt),
		})
	}
	respondJSON(c, http.StatusOK, pageListDTO{Items: items, NextPageToken: result.NextPageToken})
}

/* getPage 返回单页原图地址 */
func (h *handler) getPage(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	detail, err := h.uploads.GetPage(c.Request.Context(), userID,
		c.Param("noteId"), c.Param("pageId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, pageOriginalDTO{
		ID:          detail.ID,
		Index:       detail.Index,
		OriginalURL: detail.OriginalURL,
		ExpiresAt:   rfc3339(detail.ExpiresAt),
	})
}

/* getConversionJob 返回最近一次转换任务 */
func (h *handler) getConversionJob(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.conversions.GetJob(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toConversionJobDTO(view))
}

/* cancelConversionJob 取消排队中或处理中的任务 */
func (h *handler) cancelConversionJob(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.conversions.CancelJob(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toConversionJobDTO(view))
}

/* retryConversionJob 复用已上传页面重新提交转换，成功返回 202 */
func (h *handler) retryConversionJob(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.conversions.RetryJob(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusAccepted, toConversionJobDTO(view))
}

/* appendPagesRequest 对应 POST /v1/notes/{noteId}:appendPages */
type appendPagesRequest struct {
	UploadIDs []string `json:"uploadIds"`
}

/* appendPagesResponseDTO 只返回追加任务，符合 docs/API.md#5310 的 202 响应 */
type appendPagesResponseDTO struct {
	ConversionJob conversionJobDTO `json:"conversionJob"`
}

/* appendPages 向已有笔记追加页面并提交追加转换 */
func (h *handler) appendPages(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload appendPagesRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	result, err := h.notes.AppendPages(c.Request.Context(), noteusecase.AppendPagesInput{
		UserID:    userID,
		NoteID:    c.Param("noteId"),
		UploadIDs: payload.UploadIDs,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusAccepted, appendPagesResponseDTO{
		ConversionJob: toSubmittedJobDTO(result.Job),
	})
}

/* createFromPagesRequest 对应 POST /v1/notes:createFromPages */
type createFromPagesRequest struct {
	FolderID  string   `json:"folderId"`
	UploadIDs []string `json:"uploadIds"`
	Title     string   `json:"title"`
}

/* createFromPages 由已上传图片创建草稿并提交转换 */
func (h *handler) createFromPages(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createFromPagesRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	result, err := h.notes.CreateFromPages(c.Request.Context(), noteusecase.CreateFromPagesInput{
		UserID:    userID,
		FolderID:  payload.FolderID,
		UploadIDs: payload.UploadIDs,
		Title:     payload.Title,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, createFromPagesResponseDTO{
		Note:          toNoteSummaryDTO(result.Note),
		ConversionJob: toSubmittedJobDTO(result.Job),
		Quota:         quotaSnapshotDTO{Remaining: result.Quota.Remaining, Charged: result.Quota.Charged},
	})
}
