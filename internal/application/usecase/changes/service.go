/* 增量同步用例的依赖装配与保留期常量；构造函数拒绝缺失依赖 */
package changes

import (
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/*
 * 同步模块的分页与保留期常量。
 * 口径：pageSize 默认 200、上限 500；变更日志保留 90 天（docs/API.md#510-增量同步）。
 */
const (
	DefaultPageSize = 200
	MaxPageSize     = 500
	Retention       = 90 * 24 * time.Hour
)

/* Deps 是增量同步用例所需的端口集合 */
type Deps struct {
	Clock clock.Clock
	Log   port.ChangeLog
}

/* Service 是无状态用例编排器；可变状态全部在变更日志仓储 */
type Service struct {
	deps  Deps
	clock clock.Clock
}

/* NewService 校验必需依赖并创建服务 */
func NewService(deps Deps) (*Service, error) {
	if deps.Clock == nil {
		return nil, fmt.Errorf("增量同步用例缺少依赖: [Clock]")
	}
	if deps.Log == nil {
		return nil, fmt.Errorf("增量同步用例缺少依赖: [Log]")
	}
	return &Service{deps: deps, clock: deps.Clock}, nil
}
