/* 批量导入用例的依赖装配；构造函数拒绝缺失依赖 */
package imports

import (
	"context"
	"fmt"

	"github.com/gleannote/gleannote/internal/application/port"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
)

/* NoteCreator 是笔记用例对导入模块暴露的最小写入集合 */
type NoteCreator interface {
	CreateFromPages(ctx context.Context, input noteusecase.CreateFromPagesInput) (noteusecase.CreateFromPagesResult, error)
}

/* Deps 是导入用例所需的端口集合；目录只用于入参校验，笔记创建复用 createFromPages */
type Deps struct {
	Clock       clock.Clock
	IDs         idgen.Generator
	Folders     port.FolderRepository
	Jobs        port.ImportJobRepository
	NoteCreator NoteCreator
}

/* Service 是无状态批量导入编排器 */
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
		{"Folders", deps.Folders != nil},
		{"Jobs", deps.Jobs != nil},
		{"NoteCreator", deps.NoteCreator != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("导入用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}
