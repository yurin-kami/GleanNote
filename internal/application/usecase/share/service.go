/* 分享用例的依赖装配；公开地址基址由配置注入 */
package share

import (
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/*
 * 分享令牌与公开地址口径。
 * 令牌使用 32 字节 CSPRNG 的 Base64URL（ARCHITECTURE 8.10）；公开页地址为 {base}/s/{token}。
 */
const (
	shareTokenBytes = 32
	shareURLPrefix  = "/s/"

	/* publicPageURLTTL 与页面原图一致，10 分钟有效 */
	publicPageURLTTL = 10 * time.Minute
)

/*
 * Deps 是分享用例所需的端口集合，全部由 bootstrap 注入。
 * Notes 与 Folders 用于校验笔记可用性；Pages 与 Objects 用于公开页输出原图地址。
 */
type Deps struct {
	Clock   clock.Clock
	Notes   port.NoteRepository
	Folders port.FolderRepository
	Pages   port.NotePageRepository
	Objects port.ObjectStore
	Shares  port.ShareLinkRepository
	BaseURL string
}

/* Service 是无状态用例编排器；分享状态全部在仓储中 */
type Service struct {
	deps  Deps
	clock clock.Clock
	base  string
}

/* NewService 校验必需依赖并创建服务；缺失依赖属于装配错误，必须启动失败 */
func NewService(deps Deps) (*Service, error) {
	required := []struct {
		name string
		set  bool
	}{
		{"Clock", deps.Clock != nil},
		{"Notes", deps.Notes != nil},
		{"Folders", deps.Folders != nil},
		{"Pages", deps.Pages != nil},
		{"Objects", deps.Objects != nil},
		{"Shares", deps.Shares != nil},
		{"BaseURL", strings.TrimSpace(deps.BaseURL) != ""},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("分享用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, base: strings.TrimRight(deps.BaseURL, "/")}, nil
}

/* requireUser 拒绝缺失访问令牌上下文的调用 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
}
