/* 标签用例的依赖装配与共享常量；构造函数拒绝缺失依赖 */
package tag

import (
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
	"github.com/gleannote/gleannote/internal/platform/pagetoken"
)

/*
 * 标签模块的分页与上限常量。
 * 口径：pageSize 默认 20、上限 100；分页令牌 1 小时；单篇笔记最多 10 个标签（docs/API.md#57-标签）。
 */
const (
	defaultPageSize  = 20
	maxPageSize      = 100
	tagPageTokenTTL  = time.Hour
	tagPageScopeBase = "tags"
	MaxTagsPerNote   = 10
	orderByNoteCount = "noteCount desc"
	orderByNameAsc   = "name asc"
)

/*
 * Deps 是标签用例所需的端口集合，全部由 bootstrap 注入。
 * Notes 仅用于校验标签设置目标的可见性；关联读写统一经由 NoteTags。
 */
type Deps struct {
	Clock        clock.Clock
	IDs          idgen.Generator
	Tags         port.TagRepository
	NoteTags     port.NoteTagRepository
	Notes        port.NoteRepository
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
		{"Tags", deps.Tags != nil},
		{"NoteTags", deps.NoteTags != nil},
		{"Notes", deps.Notes != nil},
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
		return nil, fmt.Errorf("标签用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* NewPageTokenCodec 用认证 HMAC 密钥派发标签分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, tagPageTokenTTL, now)
}
