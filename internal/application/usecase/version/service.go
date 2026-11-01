/* 版本用例的依赖装配与分页口径；构造函数拒绝缺失依赖 */
package version

import (
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
	"github.com/gleannote/gleannote/internal/platform/pagetoken"
)

/* 版本列表分页口径与笔记、标签模块一致：默认 20、上限 100、令牌 1 小时 */
const (
	defaultPageSize      = 20
	maxPageSize          = 100
	versionPageTokenTTL  = time.Hour
	versionPageScopeBase = "versions"
)

/*
 * Deps 是版本用例所需的端口集合，全部由 bootstrap 注入。
 * Notes 用于校验笔记归属与恢复时写入正文；Versions 负责快照存储与保留期清理。
 */
type Deps struct {
	Clock        clock.Clock
	IDs          idgen.Generator
	Versions     port.NoteVersionRepository
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
		{"Versions", deps.Versions != nil},
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
		return nil, fmt.Errorf("版本用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* NewPageTokenCodec 复用认证 HMAC 密钥派发版本分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, versionPageTokenTTL, now)
}
