/* 目录用例的依赖装配与共享常量；构造函数拒绝缺失依赖 */
package folder

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
 * 目录模块的时间与分页常量。
 * 口径：回收站保留 30 天（PRD FR-09）；pageSize 默认 20、上限 100、负数非法；
 * 分页令牌 1 小时有效（docs/API.md#24-分页）。
 */
const (
	TrashRetention      = 30 * 24 * time.Hour
	defaultPageSize     = 20
	maxPageSize         = 100
	folderPageTokenTTL  = time.Hour
	folderPageScopeBase = "folders"
)

/* NoteLinker 由笔记模块实现，供目录删除、恢复与彻底删除时按目录联动笔记 */
type NoteLinker interface {
	CountActiveByFolder(ctx context.Context, userID string, folderIDs []string) (map[string]int, error)
	MarkDeletedByFolders(ctx context.Context, userID string, folderIDs []string,
		batchID string, now time.Time, retention time.Duration) (int, error)
	RestoreByBatch(ctx context.Context, userID, batchID string, now time.Time) (int, error)
	/* ListByFolders 返回子树内全部笔记（含回收站），供失效分享与级联删除 */
	ListByFolders(ctx context.Context, userID string, folderIDs []string) ([]*entity.Note, error)
	/* Delete 物理删除单篇笔记；仅由目录级联彻底删除调用 */
	Delete(ctx context.Context, userID, noteID string) error
}

/*
 * NoteAttachmentCleaner 是笔记附属数据的最小清理能力。
 * 标签关联、版本快照与分享记录只在目录级联彻底删除时清理，具体实现由各模块仓储提供。
 */
type NoteAttachmentCleaner interface {
	DeleteByNote(ctx context.Context, userID, noteID string) error
}

/* Deps 是目录用例所需的端口集合，全部由 bootstrap 注入 */
type Deps struct {
	Clock        clock.Clock
	IDs          idgen.Generator
	Folders      port.FolderRepository
	Notes        NoteLinker
	Pages        port.NotePageRepository
	Jobs         port.ConversionJobRepository
	TagLinks     NoteAttachmentCleaner
	Versions     NoteAttachmentCleaner
	Shares       NoteAttachmentCleaner
	Storage      port.StorageUsage
	Objects      port.ObjectStore
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
		{"Folders", deps.Folders != nil},
		{"Notes", deps.Notes != nil},
		{"Pages", deps.Pages != nil},
		{"Jobs", deps.Jobs != nil},
		{"TagLinks", deps.TagLinks != nil},
		{"Versions", deps.Versions != nil},
		{"Shares", deps.Shares != nil},
		{"Storage", deps.Storage != nil},
		{"Objects", deps.Objects != nil},
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
		return nil, fmt.Errorf("目录用例缺少依赖: %v", missing)
	}
	return &Service{deps: deps, clock: deps.Clock, ids: deps.IDs}, nil
}

/* NewPageTokenCodec 用认证 HMAC 密钥派发目录分页令牌，TTL 固定 1 小时 */
func NewPageTokenCodec(secret []byte, now clock.Clock) (*pagetoken.Codec, error) {
	return pagetoken.NewCodec(secret, folderPageTokenTTL, now)
}
