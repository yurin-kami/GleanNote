/* 账号注销的级联清理：复用共享存储锁与撤销日志，在一个事务内清空账号数据 */
package memory

import (
	"context"
	"maps"
	"strings"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* AccountPurgerDeps 汇总清理所需的各内存仓储；由 bootstrap 注入同一 Store 下的实例 */
type AccountPurgerDeps struct {
	Users         *UserRepository
	Codes         *EmailCodeRepository
	RefreshTokens *RefreshTokenRepository
	Folders       *FolderRepository
	Notes         *NoteRepository
	Pages         *NotePageRepository
	Uploads       *UploadRepository
	Jobs          *ConversionJobRepository
	Quota         *QuotaLedger
	Storage       *StorageUsage
	Tags          *TagRepository
	NoteTags      *NoteTagRepository
	Versions      *NoteVersionRepository
	Shares        *ShareLinkRepository
	Exports       *ExportJobRepository
	Imports       *ImportJobRepository
	Changes       *ChangeLogRepository
}

/* AccountPurger 是 port.AccountDataPurger 的内存实现 */
type AccountPurger struct {
	store *Store
	deps  AccountPurgerDeps
}

/* NewAccountPurger 绑定共享存储与各仓储实例 */
func NewAccountPurger(store *Store, deps AccountPurgerDeps) *AccountPurger {
	return &AccountPurger{store: store, deps: deps}
}

/*
 * PurgeForUser 清空账号的全部业务数据。
 * 事务内调用：先登记各集合的快照，失败时按逆序恢复，语义与 SQL 回滚一致。
 */
func (p *AccountPurger) PurgeForUser(ctx context.Context, userID string) error {
	release := p.store.lock(ctx)
	defer release()

	restores := p.snapshotCollections()
	p.purgeUserCollections(userID)
	p.purgeKeyedCollections(userID)
	p.store.onRollback(ctx, func() {
		for index := len(restores) - 1; index >= 0; index-- {
			restores[index]()
		}
	})
	return nil
}

/* snapshotCollections 按集合整体复制，返回逆序执行的恢复函数 */
func (p *AccountPurger) snapshotCollections() []func() {
	return []func(){
		snapshotMap(p.deps.Codes.byID),
		snapshotMap(p.deps.Codes.order),
		snapshotMap(p.deps.RefreshTokens.byHash),
		snapshotMap(p.deps.RefreshTokens.byUser),
		snapshotMap(p.deps.Folders.byUser),
		snapshotMap(p.deps.Notes.byUser),
		snapshotMap(p.deps.Pages.byNote),
		snapshotMap(p.deps.Uploads.byID),
		snapshotMap(p.deps.Jobs.byID),
		snapshotMap(p.deps.Jobs.latestByNote),
		snapshotMap(p.deps.Quota.charges),
		snapshotMap(p.deps.Quota.refunded),
		snapshotMap(p.deps.Quota.used),
		snapshotMap(p.deps.Storage.used),
		snapshotMap(p.deps.Tags.byUser),
		snapshotMap(p.deps.NoteTags.byUser),
		snapshotMap(p.deps.Versions.byUser),
		snapshotMap(p.deps.Shares.byUser),
		snapshotMap(p.deps.Shares.byToken),
		snapshotMap(p.deps.Exports.byID),
		snapshotMap(p.deps.Imports.byID),
		snapshotMap(p.deps.Changes.byUser),
		snapshotMap(p.deps.Changes.nextSeq),
		snapshotMap(p.deps.Changes.trimmed),
	}
}

/* purgeUserCollections 删除以 userID 为直接键的集合条目 */
func (p *AccountPurger) purgeUserCollections(userID string) {
	delete(p.deps.Folders.byUser, userID)
	delete(p.deps.Notes.byUser, userID)
	delete(p.deps.Tags.byUser, userID)
	delete(p.deps.NoteTags.byUser, userID)
	delete(p.deps.Versions.byUser, userID)
	delete(p.deps.Storage.used, userID)
	delete(p.deps.Changes.byUser, userID)
	delete(p.deps.Changes.nextSeq, userID)
	delete(p.deps.Changes.trimmed, userID)
}

/* purgeKeyedCollections 删除以「userID/子键」或属主字段归属的集合条目 */
func (p *AccountPurger) purgeKeyedCollections(userID string) {
	purgeOwnedPrefix(p.deps.Pages.byNote, userID)
	purgeOwnedPrefix(p.deps.Jobs.latestByNote, userID)
	purgeOwnedPrefix(p.deps.Quota.charges, userID)
	purgeOwnedPrefix(p.deps.Quota.refunded, userID)
	purgeOwnedPrefix(p.deps.Quota.used, userID)
	purgeOwnedID(p.deps.Uploads.byID, userID, func(item *entity.Upload) string { return item.UserID })
	purgeOwnedID(p.deps.Jobs.byID, userID, func(item *entity.ConversionJob) string { return item.UserID })
	purgeOwnedID(p.deps.Exports.byID, userID, func(item *entity.ExportJob) string { return item.UserID })
	purgeOwnedID(p.deps.Imports.byID, userID, func(item *entity.ImportJob) string { return item.UserID })
	p.purgeRefreshTokens(userID)
	p.purgeShares(userID)
	p.purgeCodes(userID)
}

/* purgeOwnedPrefix 删除键以 userID+"/" 开头的条目，避免 userId 前缀互相命中 */
func purgeOwnedPrefix[V any](items map[string]V, userID string) {
	prefix := userID + "/"
	for key := range items {
		if strings.HasPrefix(key, prefix) {
			delete(items, key)
		}
	}
}

/* purgeOwnedID 按属主字段删除以 ID 为键的条目，供任务与会话类集合复用 */
func purgeOwnedID[V any](items map[string]V, userID string, owner func(V) string) {
	for id, item := range items {
		if owner(item) == userID {
			delete(items, id)
		}
	}
}

/* purgeShares 同时清理账号条目与令牌索引，避免匿名读取命中已注销账号 */
func (p *AccountPurger) purgeShares(userID string) {
	links, exists := p.deps.Shares.byUser[userID]
	if !exists {
		return
	}
	for _, link := range links {
		if link.Token != "" {
			delete(p.deps.Shares.byToken, link.Token)
		}
	}
	delete(p.deps.Shares.byUser, userID)
}

/* purgeRefreshTokens 先摘除哈希索引，再删除账号维度索引 */
func (p *AccountPurger) purgeRefreshTokens(userID string) {
	for _, hash := range p.deps.RefreshTokens.byUser[userID] {
		delete(p.deps.RefreshTokens.byHash, hash)
	}
	delete(p.deps.RefreshTokens.byUser, userID)
}

/* purgeCodes 按账号邮箱清理验证码与其发送顺序索引；账号行此时仍存在 */
func (p *AccountPurger) purgeCodes(userID string) {
	user, exists := p.deps.Users.byID[userID]
	if !exists {
		return
	}
	removed := make(map[string]struct{})
	for id, code := range p.deps.Codes.byID {
		if code.Email == user.Email {
			delete(p.deps.Codes.byID, id)
			removed[id] = struct{}{}
		}
	}
	for key, ids := range p.deps.Codes.order {
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, dropped := removed[id]; !dropped {
				kept = append(kept, id)
			}
		}
		p.deps.Codes.order[key] = kept
	}
}

/* snapshotMap 复制集合内容，返回覆盖式恢复函数；仅用于删除类变更 */
func snapshotMap[K comparable, V any](items map[K]V) func() {
	copied := maps.Clone(items)
	return func() {
		clear(items)
		maps.Copy(items, copied)
	}
}

/* 确保实现端口契约 */
var _ port.AccountDataPurger = (*AccountPurger)(nil)
