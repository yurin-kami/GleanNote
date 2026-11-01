/* 转换用例的依赖装配、超时口径与构造函数 */
package conversion

import (
	"context"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
)

/*
 * 转换任务时限口径。
 * 单次识图 5 分钟（docs/API.md#63-上传与转换）；提供给供应商的原图地址 10 分钟。
 * 参考：docs/ARCHITECTURE.md#62-图片上传到转换完成
 */
const (
	ConversionTimeout = 5 * time.Minute
	visionURLTTL      = 10 * time.Minute
)

/* VersionRecorder 由版本用例实现，转换成功后写入 CONVERSION 版本 */
type VersionRecorder interface {
	Record(ctx context.Context, userID, noteID, source, content string) error
}

/* Deps 是转换用例所需的端口集合，全部由 bootstrap 注入 */
type Deps struct {
	Clock        clock.Clock
	IDs          idgen.Generator
	Notes        port.NoteRepository
	Pages        port.NotePageRepository
	Jobs         port.ConversionJobRepository
	Quota        port.QuotaLedger
	Storage      port.StorageUsage
	Queue        port.ConversionQueue
	Vision       port.VisionProvider
	Objects      port.ObjectStore
	Versions     VersionRecorder
	Transactions port.Transactor
}

/* Service 是无状态用例编排器；Worker 与 HTTP 共用同一实例 */
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
		{"Notes", deps.Notes != nil},
		{"Pages", deps.Pages != nil},
		{"Jobs", deps.Jobs != nil},
		{"Quota", deps.Quota != nil},
		{"Storage", deps.Storage != nil},
		{"Queue", deps.Queue != nil},
		{"Vision", deps.Vision != nil},
		{"Objects", deps.Objects != nil},
		{"Versions", deps.Versions != nil},
		{"Transactions", deps.Transactions != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("转换用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* requireUser 拒绝缺失访问令牌上下文的调用 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
}

/* newID 生成转换任务标识；随机源异常按内部错误上抛 */
func (s *Service) newID() (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成转换任务 ID 失败").WithCause(err)
	}
	return id, nil
}
