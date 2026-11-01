/* 笔记端点：列表、创建、详情、保存、移动、删除、恢复与彻底删除 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
)

/* tagDTO 是笔记摘要内嵌的精简标签；完整字段由 GET /v1/tags 提供 */
type tagDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

/* noteSummaryDTO 对应 openapi 的 NoteSummary；列表不输出正文 */
type noteSummaryDTO struct {
	ID             string   `json:"id"`
	FolderID       string   `json:"folderId"`
	FolderPath     string   `json:"folderPath,omitempty"`
	Title          string   `json:"title"`
	State          string   `json:"state"`
	Revision       int      `json:"revision"`
	CharCount      int      `json:"charCount"`
	PageCount      int      `json:"pageCount"`
	ThumbnailURL   string   `json:"thumbnailUrl,omitempty"`
	Tags           []tagDTO `json:"tags,omitempty"`
	IsConflictCopy bool     `json:"isConflictCopy"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

/* noteDetailDTO = NoteSummary + 正文与分享状态 */
type noteDetailDTO struct {
	noteSummaryDTO
	Content      string `json:"content"`
	ShareEnabled bool   `json:"shareEnabled"`
}

/* noteRestoreDTO 在详情基础上标注是否因原目录缺失而恢复到根级 */
type noteRestoreDTO struct {
	noteDetailDTO
	RestoredToRoot bool `json:"restoredToRoot"`
}

/* noteListDTO 是 AIP-158 集合响应 */
type noteListDTO struct {
	Items         []noteSummaryDTO `json:"items"`
	NextPageToken string           `json:"nextPageToken,omitempty"`
	TotalSize     int              `json:"totalSize"`
}

/* saveNoteResponseDTO 是 PATCH 响应；发生冲突时带 conflictCopyId */
type saveNoteResponseDTO struct {
	Note           noteDetailDTO `json:"note"`
	ConflictCopyID string        `json:"conflictCopyId,omitempty"`
}

/* toNoteSummaryDTO 映射摘要视图；标签在 P1 前保持空 */
func toNoteSummaryDTO(view noteusecase.NoteView) noteSummaryDTO {
	summary := noteSummaryDTO{
		ID:             view.ID,
		FolderID:       view.FolderID,
		FolderPath:     view.FolderPath,
		Title:          view.Title,
		State:          view.State,
		Revision:       view.Revision,
		CharCount:      view.CharCount,
		PageCount:      view.PageCount,
		ThumbnailURL:   view.ThumbnailURL,
		IsConflictCopy: view.IsConflictCopy,
		CreatedAt:      rfc3339(view.CreatedAt),
		UpdatedAt:      rfc3339(view.UpdatedAt),
	}
	/* 标签集合由版本与标签服务维护，摘要层只做结构映射，不补默认值 */
	for _, tag := range view.Tags {
		summary.Tags = append(summary.Tags, tagDTO{ID: tag.ID, Name: tag.Name})
	}
	return summary
}

/* toNoteDetailDTO 映射详情视图，正文只在该结构中出现 */
func toNoteDetailDTO(view noteusecase.NoteView) noteDetailDTO {
	return noteDetailDTO{
		noteSummaryDTO: toNoteSummaryDTO(view),
		Content:        view.Content,
		ShareEnabled:   view.ShareEnabled,
	}
}

/* listNotes 处理 GET /v1/notes；folderId 必填由用例统一校验 */
func (h *handler) listNotes(c *gin.Context) {
	/* 会话与分页参数是列表的唯一入口约束，folderId 由用例层统一校验 */
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
	/* 查询条件原样透传，过滤与排序语义全部落在用例层 */
	result, err := h.notes.ListNotes(c.Request.Context(), noteusecase.ListNotesInput{
		UserID:       userID,
		FolderID:     c.Query("folderId"),
		State:        c.Query("state"),
		UpdatedAfter: c.Query("updatedAfter"),
		OrderBy:      c.Query("orderBy"),
		PageToken:    c.Query("pageToken"),
		PageSize:     pageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	/* 列表不输出正文，逐条转为摘要后按 AIP-158 集合结构返回 */
	items := make([]noteSummaryDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toNoteSummaryDTO(item))
	}
	respondJSON(c, http.StatusOK, noteListDTO{
		Items:         items,
		NextPageToken: result.NextPageToken,
		TotalSize:     result.TotalSize,
	})
}

/* createNoteRequest 对应 POST /v1/notes；clientCreatedAt 供离线补录 */
type createNoteRequest struct {
	FolderID        string `json:"folderId"`
	Title           string `json:"title"`
	Content         string `json:"content"`
	ClientCreatedAt string `json:"clientCreatedAt"`
}

/* createNote 新建空白笔记并返回 201 详情 */
func (h *handler) createNote(c *gin.Context) {
	/* 先校验会话与请求体，避免把非法输入带到用例层 */
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload createNoteRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	/* 空白笔记的字段校验与初始版本写入由用例层完成 */
	view, err := h.notes.CreateNote(c.Request.Context(), noteusecase.CreateNoteInput{
		UserID:          userID,
		FolderID:        payload.FolderID,
		Title:           payload.Title,
		Content:         payload.Content,
		ClientCreatedAt: payload.ClientCreatedAt,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, toNoteDetailDTO(view))
}

/* getNote 处理 GET /v1/notes/{noteId}；回收站中的笔记同样可读 */
func (h *handler) getNote(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.notes.GetNote(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toNoteDetailDTO(view))
}

/* updateNoteRequest 对应 PATCH /v1/notes/{noteId}；标题与正文用指针区分「未提供」 */
type updateNoteRequest struct {
	Title      *string `json:"title"`
	Content    *string `json:"content"`
	Revision   int     `json:"revision"`
	SaveSource string  `json:"saveSource"`
}

/* updateNote 保存标题或正文，修订号落后时返回冲突副本标识 */
func (h *handler) updateNote(c *gin.Context) {
	/* 标题与正文用指针区分「未提供」，因此必须先会话后绑定 */
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload updateNoteRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	/* 修订号由客户端回传，冲突检测与副本生成在用例层完成 */
	result, err := h.notes.UpdateNote(c.Request.Context(), noteusecase.UpdateNoteInput{
		UserID:     userID,
		NoteID:     c.Param("noteId"),
		Title:      payload.Title,
		Content:    payload.Content,
		Revision:   payload.Revision,
		SaveSource: payload.SaveSource,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	/* 响应同时返回最新详情与冲突副本 ID，供客户端引导用户处理冲突 */
	respondJSON(c, http.StatusOK, saveNoteResponseDTO{
		Note:           toNoteDetailDTO(result.Note),
		ConflictCopyID: result.ConflictCopyID,
	})
}

/* deleteNote 软删除笔记并返回回收站摘要 */
func (h *handler) deleteNote(c *gin.Context) {
	/* 软删除只登记回收站批次与到期时间，真正的数据清理交给 purge 任务 */
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	result, err := h.notes.DeleteNote(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	/* 单篇删除的 noteCount 恒为 1，批量删除由目录端点返回真实数量 */
	respondJSON(c, http.StatusOK, deleteResultDTO{
		DeleteBatchID: result.DeleteBatchID,
		NoteCount:     1,
		PurgeAt:       rfc3339(result.PurgeAt),
	})
}

/* moveNoteRequest 对应 POST /v1/notes/{noteId}:move */
type moveNoteRequest struct {
	FolderID string `json:"folderId"`
}

/* moveNote 移动笔记并返回摘要视图 */
func (h *handler) moveNote(c *gin.Context) {
	/* 目标目录的归属与循环校验在用例层完成，这里只负责会话与请求体 */
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload moveNoteRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	/* 移动不改变正文与修订号，返回摘要即可 */
	view, err := h.notes.MoveNote(c.Request.Context(), noteusecase.MoveNoteInput{
		UserID:   userID,
		NoteID:   c.Param("noteId"),
		FolderID: payload.FolderID,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toNoteSummaryDTO(view))
}

/* undeleteNote 恢复回收站中的笔记，并显式标注是否回落到根级 */
func (h *handler) undeleteNote(c *gin.Context) {
	/* 恢复是否回落到根级由用例层判定，响应显式告知客户端 */
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	result, err := h.notes.RestoreNote(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, noteRestoreDTO{
		noteDetailDTO:  toNoteDetailDTO(result.Note),
		RestoredToRoot: result.RestoredToRoot,
	})
}

/* expungeNote 彻底删除回收站中的笔记，成功返回 204 */
func (h *handler) expungeNote(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.notes.ExpungeNote(c.Request.Context(), userID, c.Param("noteId")); err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}

/* searchItemDTO 是单条搜索结果；snippet 仅含 <em> 标记 */
type searchItemDTO struct {
	NoteID     string `json:"noteId"`
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	FolderPath string `json:"folderPath,omitempty"`
	UpdatedAt  string `json:"updatedAt"`
}

/* searchListDTO 是搜索集合响应，字段与 SearchResultList 对齐 */
type searchListDTO struct {
	Items         []searchItemDTO `json:"items"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
	TotalSize     int             `json:"totalSize"`
}

/*
 * searchNotes 处理 GET /v1/notes:search。
 * tagIds 允许重复传递；过滤语义为同时包含全部标签（docs/API.md#5311）。
 */
func (h *handler) searchNotes(c *gin.Context) {
	/* 搜索同样需要有效会话与分页参数，否则回落到默认页大小 */
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
	/* tagIds 以重复参数传递，查询与过滤语义统一落在用例层 */
	result, err := h.notes.SearchNotes(c.Request.Context(), noteusecase.SearchNotesInput{
		UserID:    userID,
		Query:     c.Query("query"),
		FolderID:  c.Query("folderId"),
		TagIDs:    c.QueryArray("tagIds"),
		PageToken: c.Query("pageToken"),
		PageSize:  pageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	/* 片段已由用例层转义，仅保留 <em> 高亮标记，这里只做字段映射 */
	items := make([]searchItemDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, searchItemDTO{
			NoteID:     item.NoteID,
			Title:      item.Title,
			Snippet:    item.Snippet,
			FolderPath: item.FolderPath,
			UpdatedAt:  rfc3339(item.UpdatedAt),
		})
	}
	/* 集合响应与列表端点保持同一形状，便于客户端复用分页逻辑 */
	respondJSON(c, http.StatusOK, searchListDTO{
		Items:         items,
		NextPageToken: result.NextPageToken,
		TotalSize:     result.TotalSize,
	})
}
