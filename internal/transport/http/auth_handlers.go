/* 认证端点的请求 DTO 与处理器：只做协议转换，业务规则全部落在认证用例 */
package httptransport

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	authusecase "github.com/gleannote/gleannote/internal/application/usecase/auth"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* sendCodeRequest 对应 POST /v1/auth:sendCode */
type sendCodeRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

/* sendCode 发送验证码；成功无响应体，固定 204 */
func (h *handler) sendCode(c *gin.Context) {
	var payload sendCodeRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	err := h.auth.SendCode(c.Request.Context(), authusecase.SendCodeInput{
		Email:   payload.Email,
		Purpose: payload.Purpose,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}

/* registerRequest 对应 POST /v1/auth:register */
type registerRequest struct {
	Email      string            `json:"email"`
	Code       string            `json:"code"`
	Password   string            `json:"password"`
	Nickname   string            `json:"nickname"`
	DeviceID   string            `json:"deviceId"`
	Agreements agreementsRequest `json:"agreements"`
}

/* agreementsRequest 两项协议必须同时为 true，由用例统一判定并给出错误码 */
type agreementsRequest struct {
	UserAgreement bool `json:"userAgreement"`
	PrivacyPolicy bool `json:"privacyPolicy"`
}

/* register 注册并直接返回登录态；成功状态码为 201 */
func (h *handler) register(c *gin.Context) {
	var payload registerRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	session, err := h.auth.Register(c.Request.Context(), authusecase.RegisterInput{
		Email:    payload.Email,
		Code:     payload.Code,
		Password: payload.Password,
		Nickname: payload.Nickname,
		DeviceID: deviceIDFrom(c, payload.DeviceID),
		Agreements: authusecase.Agreements{
			UserAgreement: payload.Agreements.UserAgreement,
			PrivacyPolicy: payload.Agreements.PrivacyPolicy,
		},
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, toSessionDTO(session))
}

/* deviceIDFrom 优先使用请求体字段，缺省时回落到 X-Device-Id 头 */
func deviceIDFrom(c *gin.Context, fromBody string) string {
	if strings.TrimSpace(fromBody) != "" {
		return fromBody
	}
	return c.GetHeader("X-Device-Id")
}

/* loginRequest 对应 POST /v1/auth:login */
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	DeviceID string `json:"deviceId"`
}

/* login 校验邮箱口令并返回新的登录态 */
func (h *handler) login(c *gin.Context) {
	var payload loginRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	session, err := h.auth.Login(c.Request.Context(), authusecase.LoginInput{
		Email:    payload.Email,
		Password: payload.Password,
		DeviceID: deviceIDFrom(c, payload.DeviceID),
		ClientIP: c.ClientIP(),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toSessionDTO(session))
}

/* refreshRequest 对应 POST /v1/auth:refresh */
type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
	DeviceID     string `json:"deviceId"`
}

/* refresh 用刷新令牌换取新的令牌对；旧令牌在同一次调用内失效 */
func (h *handler) refresh(c *gin.Context) {
	var payload refreshRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	session, err := h.auth.Refresh(c.Request.Context(), authusecase.RefreshInput{
		RefreshToken: payload.RefreshToken,
		DeviceID:     deviceIDFrom(c, payload.DeviceID),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toSessionDTO(session))
}

/* logoutRequest 对应 POST /v1/auth:logout；refreshToken 为契约必填 */
type logoutRequest struct {
	RefreshToken string `json:"refreshToken"`
	AllDevices   bool   `json:"allDevices"`
}

/* logout 结束当前登录态；访问令牌加入黑名单，刷新令牌按设备或全量吊销 */
func (h *handler) logout(c *gin.Context) {
	claims, ok := accessClaimsFrom(c)
	if !ok {
		respondError(c, errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文"))
		return
	}
	var payload logoutRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	if strings.TrimSpace(payload.RefreshToken) == "" {
		respondError(c, errs.New(errs.CodeValidationFailed, "refreshToken 不能为空"))
		return
	}
	err := h.auth.Logout(c.Request.Context(), authusecase.LogoutInput{
		UserID:          claims.UserID,
		TokenID:         claims.TokenID,
		AccessExpiresAt: claims.ExpiresAt,
		RefreshToken:    payload.RefreshToken,
		AllDevices:      payload.AllDevices,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}

/* resetPasswordRequest 对应 POST /v1/auth:resetPassword */
type resetPasswordRequest struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"newPassword"`
}

/* resetPassword 通过邮箱验证码重置密码；成功后全部设备需要重新登录 */
func (h *handler) resetPassword(c *gin.Context) {
	var payload resetPasswordRequest
	if err := bindJSON(c, &payload); err != nil {
		respondError(c, err)
		return
	}
	err := h.auth.ResetPassword(c.Request.Context(), authusecase.ResetPasswordInput{
		Email:       payload.Email,
		Code:        payload.Code,
		NewPassword: payload.NewPassword,
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}
