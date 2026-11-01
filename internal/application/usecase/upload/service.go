/* 上传用例的依赖装配、时限常量与构造函数 */
package upload

import (
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
	"github.com/gleannote/gleannote/internal/platform/pagetoken"
)

/*
 * 上传与页面读取的时限口径。
 * 上传会话 30 分钟；原图与缩略图地址 10 分钟；页面分页令牌 1 小时。
 * 参考：docs/API.md#541-post-v1uploads、docs/API.md#544-get-v1notesnoteidpagespageid
 */
const (
	UploadSessionTTL = 30 * time.Minute
	MediaURLTTL      = 10 * time.Minute
	pageTokenTTL     = time.Hour
	pageScopeBase    = "pages"
	defaultPageSize  = 20
	maxPageSize      = 100
)

/* Deps 是上传用例所需的端口集合，全部由 bootstrap 注入 */
type Deps struct {
	Clock        clock.Clock
	IDs          idgen.Generator
	Uploads      port.UploadRepository
	Notes        port.NoteRepository
	Pages        port.NotePageRepository
	Objects      port.ObjectStore
	Images       port.ImageProcessor
	Transactions port.Transactor
	PageTokens   *pagetoken.Codec
}

/* Service 是无状态用例编排器；可变状态全部在仓储与端口中 */
type Service struct {
	deps  Deps
	clock clock.Clock
	ids   idgen.Generator
}

/* NewService 校验必需依赖并创建服务；缺失依赖属于装配错误，必须启动失败 */
func NewService(deps Deps) (*Service, error) {
	required := []struct {
		name string
		set  bool
	}{
		{"Clock", deps.Clock != nil},
		{"IDs", deps.IDs != nil},
		{"Uploads", deps.Uploads != nil},
		{"Notes", deps.Notes != nil},
		{"Pages", deps.Pages != nil},
		{"Objects", deps.Objects != nil},
		{"Images", deps.Images != nil},
		{"Transactions", deps.Transactions != nil},
		{"PageTokens", deps.PageTokens != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("上传用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* NewPageTokenCodec 用认证 HMAC 密钥派发页面分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, pageTokenTTL, now)
}

/* requireUser 拒绝缺失访问令牌上下文的调用，语义与认证模块一致 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
}

/* newID 生成上传会话标识；随机源异常按内部错误上抛 */
func (s *Service) newID() (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成上传 ID 失败").WithCause(err)
	}
	return id, nil
}
