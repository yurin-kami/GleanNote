/* 回收站用例的依赖装配与共享常量；构造函数拒绝缺失依赖 */
package trash

import (
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/pagetoken"
)

/* 回收站分页常量：pageSize 默认 20、上限 100；令牌 1 小时（docs/API.md#24-分页） */
const (
	defaultPageSize   = 20
	maxPageSize       = 100
	trashPageTokenTTL = time.Hour
	trashPageScope    = "trash"
)

/* 回收站条目类型，取值与 TrashItemType 一致（docs/API.md#32-枚举字典） */
const (
	TrashItemFolder = "FOLDER"
	TrashItemNote   = "NOTE"
)

/*
 * Deps 是回收站用例所需的端口集合。
 * 回收站是跨类型聚合视图：目录与笔记各自按删除批次归组，不提供写操作。
 * 参考：docs/API.md#56-回收站、PRD FR-09
 */
type Deps struct {
	Clock      clock.Clock
	Folders    port.FolderRepository
	Notes      port.NoteRepository
	PageTokens *pagetoken.Codec
}

/* Service 是无状态用例编排器；可变状态全部在仓储中 */
type Service struct {
	deps  Deps
	clock clock.Clock
}

/* NewService 校验必需依赖并创建服务；缺失依赖属于装配错误，必须启动失败 */
func NewService(deps Deps) (*Service, error) {
	required := []struct {
		name string
		set  bool
	}{
		{"Clock", deps.Clock != nil},
		{"Folders", deps.Folders != nil},
		{"Notes", deps.Notes != nil},
		{"PageTokens", deps.PageTokens != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("回收站用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock}, nil
}

/* NewPageTokenCodec 用认证 HMAC 密钥派发回收站分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, trashPageTokenTTL, now)
}
