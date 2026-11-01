/* 增量同步端点：按游标返回变更集，DELETE 事件以墓碑载荷下发 */
package httptransport

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	changesusecase "github.com/gleannote/gleannote/internal/application/usecase/changes"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* syncChangeDTO 对应 openapi 的 SyncChange；墓碑事件的 payload 为 null */
type syncChangeDTO struct {
	Seq          int64  `json:"seq"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Op           string `json:"op"`
	Revision     int    `json:"revision,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
	Payload      any    `json:"payload"`
}

/* syncResponseDTO 对应 openapi 的 SyncResponse */
type syncResponseDTO struct {
	Changes    []syncChangeDTO `json:"changes"`
	NextCursor int64           `json:"nextCursor"`
	HasMore    bool            `json:"hasMore"`
}

/* listSyncChanges 返回 since 之后的变更；pageSize 缺省 200、上限 500 */
func (h *handler) listSyncChanges(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	since, err := parseSinceQuery(c)
	if err != nil {
		respondError(c, err)
		return
	}
	pageSize, err := parsePageSizeQuery(c)
	if err != nil {
		respondError(c, err)
		return
	}
	result, err := h.changes.ListChanges(c.Request.Context(), changesusecase.ListChangesInput{
		UserID:        userID,
		Since:         since,
		PageSize:      pageSize,
		ResourceTypes: c.QueryArray("resourceTypes"),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	dtos := make([]syncChangeDTO, 0, len(result.Changes))
	for _, item := range result.Changes {
		dtos = append(dtos, syncChangeDTO{
			Seq:          item.Seq,
			ResourceType: item.ResourceType,
			ResourceID:   item.ResourceID,
			Op:           item.Op,
			Revision:     item.Revision,
			UpdatedAt:    rfc3339(item.UpdatedAt),
			Payload:      item.Payload,
		})
	}
	respondJSON(c, http.StatusOK, syncResponseDTO{
		Changes:    dtos,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	})
}

/* parseSinceQuery 解析游标；缺省 0 表示全量初始化 */
func parseSinceQuery(c *gin.Context) (int64, error) {
	raw := c.Query("since")
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errs.New(errs.CodeValidationFailed, "since 必须为非负整数")
	}
	return parsed, nil
}
