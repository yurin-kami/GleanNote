/* 笔记用例的依赖装配与共享常量；构造函数拒绝缺失依赖 */
package note

import (
	"context"
	"fmt"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/platform/clock"
	"github.com/gleannote/gleannote/internal/platform/idgen"
	"github.com/gleannote/gleannote/internal/platform/pagetoken"
)

/*
 * 笔记模块的时间与分页常量。
 * 口径：回收站保留 30 天（PRD FR-09）；pageSize 默认 20、上限 100；
 * 分页令牌与缩略图地址 1 小时 / 10 分钟（docs/API.md#24-分页、docs/API.md#29-图片上传协议）。
 */
const (
	NoteTrashRetention = 30 * 24 * time.Hour
	defaultPageSize    = 20
	maxPageSize        = 100
	notePageTokenTTL   = time.Hour
	notePageScopeBase  = "notes"
	thumbnailURLTTL    = 10 * time.Minute
)

/* FolderRestorer 由目录用例实现，供笔记恢复时同时恢复必要的上级目录 */
type FolderRestorer interface {
	RestoreNoteFolder(ctx context.Context, userID, folderID string) (FolderRestore, error)
}

/*
 * TagLinks 是标签关联的最小读写集合。
 * 笔记模块只负责回填标签与清理关联，标签本体与重名规则由标签用例负责。
 */
type TagLinks interface {
	TagsByNotes(ctx context.Context, userID string, noteIDs []string) (map[string][]*entity.Tag, error)
	DeleteByNote(ctx context.Context, userID, noteID string) error
	NoteIDsByTags(ctx context.Context, userID string, tagIDs []string) ([]string, error)
}

/*
 * VersionRecorder 是版本模块对外暴露的最小写入集合。
 * 笔记模块在保存、冲突副本与彻底删除时调用，版本保留策略由版本用例负责。
 */
type VersionRecorder interface {
	Record(ctx context.Context, userID, noteID, source, content string) error
	DeleteByNote(ctx context.Context, userID, noteID string) error
}

/*
 * ShareLinks 是分享状态的最小读写集合。
 * 笔记模块只负责详情回填与删除时失效链接，公开读取由分享用例负责。
 */
type ShareLinks interface {
	EnabledByNotes(ctx context.Context, userID string, noteIDs []string) (map[string]bool, error)
	DeleteByNote(ctx context.Context, userID, noteID string) error
}

/*
 * Deps 是笔记用例所需的端口集合，全部由 bootstrap 注入。
 * 页面、上传、任务、额度与队列端口只服务于 createFromPages 与彻底删除；
 * 对象存储仅用于签发缩略图地址与清理彻底删除后的对象。
 */
type Deps struct {
	Clock          clock.Clock
	IDs            idgen.Generator
	Notes          port.NoteRepository
	Folders        port.FolderRepository
	FolderRestorer FolderRestorer
	Uploads        port.UploadRepository
	Pages          port.NotePageRepository
	Jobs           port.ConversionJobRepository
	Quota          port.QuotaLedger
	Storage        port.StorageUsage
	Queue          port.ConversionQueue
	Objects        port.ObjectStore
	TagLinks       TagLinks
	Versions       VersionRecorder
	Shares         ShareLinks
	Transactions   port.Transactor
	PageTokens     *pagetoken.Codec
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
		{"Notes", deps.Notes != nil},
		{"Folders", deps.Folders != nil},
		{"FolderRestorer", deps.FolderRestorer != nil},
		{"Uploads", deps.Uploads != nil},
		{"Pages", deps.Pages != nil},
		{"Jobs", deps.Jobs != nil},
		{"Quota", deps.Quota != nil},
		{"Storage", deps.Storage != nil},
		{"Queue", deps.Queue != nil},
		{"Objects", deps.Objects != nil},
		{"TagLinks", deps.TagLinks != nil},
		{"Versions", deps.Versions != nil},
		{"Shares", deps.Shares != nil},
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
		return nil, fmt.Errorf("笔记用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* NewPageTokenCodec 用认证 HMAC 密钥派发笔记分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, notePageTokenTTL, now)
}
