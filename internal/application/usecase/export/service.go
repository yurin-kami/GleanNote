/* 导出用例的依赖装配；构造函数拒绝缺失依赖 */
package export

import (
	"fmt"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
)

/*
 * Deps 是导出用例所需的端口集合。
 * 笔记、目录与页面只读；对象存储负责写入归档与签发下载地址；PDF 渲染器负责排版。
 */
type Deps struct {
	Clock   clock.Clock
	IDs     idgen.Generator
	Notes   port.NoteRepository
	Folders port.FolderRepository
	Pages   port.NotePageRepository
	Objects port.ObjectStore
	Jobs    port.ExportJobRepository
	PDF     port.PDFRenderer
}

/* Service 是无状态导出用例编排器 */
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
		{"Folders", deps.Folders != nil},
		{"Pages", deps.Pages != nil},
		{"Objects", deps.Objects != nil},
		{"Jobs", deps.Jobs != nil},
		{"PDF", deps.PDF != nil},
	}
	missing := make([]string, 0, len(required))
	for _, item := range required {
		if !item.set {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("导出用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}
