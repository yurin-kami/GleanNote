/* 用户与账号端点：当前用户、昵称修改与注销 */
package httptransport

import (
	"net/http"

	"github.com/gin-gonic/gin"

	authusecase "github.com/gleannote/gleannote/internal/application/usecase/auth"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* unauthorized 是路由遗漏鉴权上下文时的统一响应，正常不会触发 */
func unauthorized(c *gin.Context) {
	respondError(c, errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文"))
}

/* getMe 返回当前账号资料；资源不存在统一由用例给出 404 */
func (h *handler) getMe(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.auth.GetUser(c.Request.Context(), userID)
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toUserDTO(view))
}

/* nicknameRequest 对应 PATCH /v1/users/me */
type nicknameRequest struct {
	Nickname string `json:"nickname"`
}

/* updateMe 修改昵称并返回最新资料 */
func (h *handler) updateMe(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload nicknameRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	view, err := h.auth.UpdateNickname(c.Request.Context(), userID, payload.Nickname)
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toUserDTO(view))
}

/* quotaStorageDTO 是额度响应中的云端容量段 */
type quotaStorageDTO struct {
	UsedBytes   int64 `json:"usedBytes"`
	LimitBytes  int64 `json:"limitBytes"`
	UsedPercent int   `json:"usedPercent"`
}

/* quotaDTO 对应 GET /v1/users/me/quota 的响应结构 */
type quotaDTO struct {
	QuotaDate         string          `json:"quotaDate"`
	DailyLimit        int             `json:"dailyLimit"`
	Remaining         int             `json:"remaining"`
	ResetAt           string          `json:"resetAt"`
	ConversionRunning bool            `json:"conversionRunning"`
	Storage           quotaStorageDTO `json:"storage"`
}

/* getQuota 返回当日额度与云端容量；口径由转换用例统一计算 */
func (h *handler) getQuota(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.conversions.QuotaOverview(c.Request.Context(), userID)
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, quotaDTO{
		QuotaDate:         view.QuotaDate,
		DailyLimit:        view.DailyLimit,
		Remaining:         view.Remaining,
		ResetAt:           rfc3339(view.ResetAt),
		ConversionRunning: view.ConversionRunning,
		Storage: quotaStorageDTO{
			UsedBytes:   view.Storage.UsedBytes,
			LimitBytes:  view.Storage.LimitBytes,
			UsedPercent: view.Storage.UsedPercent,
		},
	})
}

/* deleteAccountRequest 对应 DELETE /v1/users/me；confirm 必须为 true 才执行 */
type deleteAccountRequest struct {
	Code    string `json:"code"`
	Confirm bool   `json:"confirm"`
}

/* deleteMe 注销账号：验证码二次确认后物理删除，成功后固定 204 */
func (h *handler) deleteMe(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	var payload deleteAccountRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	err := h.auth.DeleteAccount(c.Request.Context(), authusecase.DeleteAccountInput{
		UserID:  userID,
		Code:    payload.Code,
		Confirm: payload.Confirm,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}
