/* HTTP 处理器共享的 DTO 与映射：会话、用户视图与时间格式 */
package httptransport

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	authusecase "github.com/gleannote/gleannote/internal/application/usecase/auth"
	changesusecase "github.com/gleannote/gleannote/internal/application/usecase/changes"
	conversionusecase "github.com/gleannote/gleannote/internal/application/usecase/conversion"
	exportusecase "github.com/gleannote/gleannote/internal/application/usecase/export"
	folderusecase "github.com/gleannote/gleannote/internal/application/usecase/folder"
	importsusecase "github.com/gleannote/gleannote/internal/application/usecase/imports"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	shareusecase "github.com/gleannote/gleannote/internal/application/usecase/share"
	tagusecase "github.com/gleannote/gleannote/internal/application/usecase/tag"
	trashusecase "github.com/gleannote/gleannote/internal/application/usecase/trash"
	uploadusecase "github.com/gleannote/gleannote/internal/application/usecase/upload"
	versionusecase "github.com/gleannote/gleannote/internal/application/usecase/version"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* handler 持有账号、目录、笔记、上传、转换、标签、回收站、版本与分享用例，所有业务端点都经由它访问应用层 */
type handler struct {
	auth        *authusecase.Service
	folders     *folderusecase.Service
	notes       *noteusecase.Service
	uploads     *uploadusecase.Service
	conversions *conversionusecase.Service
	tags        *tagusecase.Service
	trash       *trashusecase.Service
	versions    *versionusecase.Service
	shares      *shareusecase.Service
	changes     *changesusecase.Service
	exports     *exportusecase.Service
	imports     *importsusecase.Service
}

/* userDTO 与 openapi 的 User 保持一致；时间统一 UTC + RFC3339 */
type userDTO struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Nickname      string `json:"nickname"`
	EmailVerified bool   `json:"emailVerified"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

/* sessionDTO 是注册、登录与刷新的统一响应体 */
type sessionDTO struct {
	User         userDTO `json:"user"`
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	ExpiresIn    int     `json:"expiresIn"`
	TokenType    string  `json:"tokenType"`
}

/* toUserDTO 只暴露稳定字段，密码哈希与删除标记永不出现在响应中 */
func toUserDTO(view authusecase.UserView) userDTO {
	return userDTO{
		ID:            view.ID,
		Email:         view.Email,
		Nickname:      view.Nickname,
		EmailVerified: view.EmailVerified,
		CreatedAt:     rfc3339(view.CreatedAt),
		UpdatedAt:     rfc3339(view.UpdatedAt),
	}
}

/* toSessionDTO 把用例返回的登录态映射为契约结构 */
func toSessionDTO(session authusecase.Session) sessionDTO {
	return sessionDTO{
		User:         toUserDTO(session.User),
		AccessToken:  session.AccessToken,
		RefreshToken: session.RefreshToken,
		ExpiresIn:    session.ExpiresIn,
		TokenType:    session.TokenType,
	}
}

/* rfc3339 固定输出带 Z 的秒级时间，避免纳秒与本地时区泄露到契约 */
func rfc3339(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

/* parsePageSizeQuery 解析公共 pageSize 参数；非法值统一为 VALIDATION_FAILED */
func parsePageSizeQuery(c *gin.Context) (int, error) {
	raw := c.Query("pageSize")
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0, errs.New(errs.CodeValidationFailed, "pageSize 必须为非负整数")
	}
	return parsed, nil
}
