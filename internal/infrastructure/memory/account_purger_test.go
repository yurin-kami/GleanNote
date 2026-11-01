/* 账号级联清理的内存实现测试：覆盖按属主、前缀键与令牌索引删除，以及事务回滚 */
package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* newPurgerFixture 构造共享同一 Store 的全部仓储并返回清理器 */
func newPurgerFixture(store *Store) (AccountPurgerDeps, *AccountPurger) {
	changes := NewChangeLogRepository(store)
	store.SetChangeLog(changes)
	tags := NewTagRepository(store)
	notes := NewNoteRepository(store)
	deps := AccountPurgerDeps{
		Users:         NewUserRepository(store),
		Codes:         NewEmailCodeRepository(store),
		RefreshTokens: NewRefreshTokenRepository(store),
		Folders:       NewFolderRepository(store),
		Notes:         notes,
		Pages:         NewNotePageRepository(store),
		Uploads:       NewUploadRepository(store),
		Jobs:          NewConversionJobRepository(store),
		Quota:         NewQuotaLedger(store),
		Storage:       NewStorageUsage(store),
		Tags:          tags,
		NoteTags:      NewNoteTagRepository(store, tags, notes),
		Versions:      NewNoteVersionRepository(store),
		Shares:        NewShareLinkRepository(store),
		Exports:       NewExportJobRepository(store),
		Imports:       NewImportJobRepository(store),
		Changes:       changes,
	}
	return deps, NewAccountPurger(store, deps)
}

/* seedOwnedData 直接写入各集合中归属 userID 的最小可用记录 */
func seedOwnedData(deps AccountPurgerDeps, userID string) {
	email := userID + "@example.com"
	deps.Users.byID[userID] = &entity.User{ID: userID, Email: email}
	deps.Codes.byID["c-"+userID] = &entity.EmailCode{ID: "c-" + userID, Email: email}
	deps.Codes.order[codeKey(email, entity.PurposeRegister)] = []string{"c-" + userID}
	deps.RefreshTokens.byHash["h-"+userID] = &entity.RefreshToken{UserID: userID, TokenHash: "h-" + userID}
	deps.RefreshTokens.byUser[userID] = []string{"h-" + userID}
	deps.Folders.byUser[userID] = []*entity.Folder{{ID: "f-" + userID, UserID: userID}}
	deps.Notes.byUser[userID] = []*entity.Note{{ID: "n-" + userID, UserID: userID}}
	deps.Tags.byUser[userID] = []*entity.Tag{{ID: "t-" + userID, UserID: userID}}
	deps.NoteTags.byUser[userID] = map[string][]string{"n-" + userID: {"t-" + userID}}
	deps.Versions.byUser[userID] = []*entity.NoteVersion{{ID: "v-" + userID, UserID: userID}}
	deps.Pages.byNote[noteBucketKey(userID, "n-"+userID)] = []*entity.NotePage{{
		ID: "p-" + userID, UserID: userID, NoteID: "n-" + userID,
	}}
	deps.Uploads.byID["u-"+userID] = &entity.Upload{ID: "u-" + userID, UserID: userID}
	deps.Jobs.byID["j-"+userID] = &entity.ConversionJob{ID: "j-" + userID, UserID: userID}
	deps.Jobs.latestByNote[noteBucketKey(userID, "n-"+userID)] = "j-" + userID
	deps.Quota.charges[ledgerKey(userID, "j-"+userID)] = chargeEntry{quotaDate: "2026-10-28", amount: 1}
	deps.Quota.refunded[ledgerKey(userID, "j-"+userID)] = struct{}{}
	deps.Quota.used[usageKey(userID, "2026-10-28")] = 1
	deps.Storage.used[userID] = 1024
	deps.Exports.byID["e-"+userID] = &entity.ExportJob{ID: "e-" + userID, UserID: userID}
	deps.Imports.byID["i-"+userID] = &entity.ImportJob{ID: "i-" + userID, UserID: userID}
	deps.Changes.byUser[userID] = []*entity.ChangeEvent{{UserID: userID, Seq: 1}}
	deps.Changes.nextSeq[userID] = 1
	deps.Shares.byUser[userID] = map[string]*entity.ShareLink{
		"n-" + userID: {UserID: userID, NoteID: "n-" + userID, Token: "tok-" + userID},
	}
	deps.Shares.byToken["tok-"+userID] = tokenRef{userID: userID, noteID: "n-" + userID}
}

/* seedForeignData 写入相邻账号，用于验证前缀键不会误删 user-a-extra 之类的账号 */
func seedForeignData(deps AccountPurgerDeps) {
	seedOwnedData(deps, "user-a-extra")
	deps.Pages.byNote[noteBucketKey("user-a-extra", "n-user-a")] = []*entity.NotePage{{
		ID: "p-extra", UserID: "user-a-extra", NoteID: "n-user-a",
	}}
}

/* purgeCoverage 返回账号仍残留与仍缺失的集合名，用于断言清理范围 */
func purgeCoverage(deps AccountPurgerDeps, userID string) (remaining, missing []string) {
	email := userID + "@example.com"
	jobID := ledgerKey(userID, "j-"+userID)
	checks := []struct {
		name    string
		present bool
	}{
		{"folders", len(deps.Folders.byUser[userID]) > 0},
		{"notes", len(deps.Notes.byUser[userID]) > 0},
		{"tags", len(deps.Tags.byUser[userID]) > 0},
		{"note_tags", len(deps.NoteTags.byUser[userID]) > 0},
		{"versions", len(deps.Versions.byUser[userID]) > 0},
		{"pages", len(deps.Pages.byNote[noteBucketKey(userID, "n-"+userID)]) > 0},
		{"uploads", deps.Uploads.byID["u-"+userID] != nil},
		{"jobs", deps.Jobs.byID["j-"+userID] != nil},
		{"job_index", deps.Jobs.latestByNote[noteBucketKey(userID, "n-"+userID)] != ""},
		{"quota_charges", hasKey(deps.Quota.charges, jobID)},
		{"quota_refunded", hasKey(deps.Quota.refunded, jobID)},
		{"quota_used", hasKey(deps.Quota.used, usageKey(userID, "2026-10-28"))},
		{"storage", deps.Storage.used[userID] > 0},
		{"exports", deps.Exports.byID["e-"+userID] != nil},
		{"imports", deps.Imports.byID["i-"+userID] != nil},
		{"changes", len(deps.Changes.byUser[userID]) > 0},
		{"change_seq", deps.Changes.nextSeq[userID] != 0},
		{"refresh_tokens", deps.RefreshTokens.byHash["h-"+userID] != nil},
		{"refresh_index", len(deps.RefreshTokens.byUser[userID]) > 0},
		{"email_codes", deps.Codes.byID["c-"+userID] != nil},
		{"code_order", len(deps.Codes.order[codeKey(email, entity.PurposeRegister)]) > 0},
		{"shares", deps.Shares.byUser[userID] != nil},
		{"share_token", deps.Shares.byToken["tok-"+userID].userID != ""},
	}
	remaining = make([]string, 0, len(checks))
	missing = make([]string, 0, len(checks))
	for _, check := range checks {
		if check.present {
			remaining = append(remaining, check.name)
			continue
		}
		missing = append(missing, check.name)
	}
	return remaining, missing
}

/* hasKey 判断泛型集合是否仍包含指定键 */
func hasKey[V any](items map[string]V, key string) bool {
	_, exists := items[key]
	return exists
}

/* TestAccountPurgerPurgesOnlyTargetUser 校验清理范围与账号边界 */
func TestAccountPurgerPurgesOnlyTargetUser(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	deps, purger := newPurgerFixture(store)
	seedOwnedData(deps, "user-a")
	seedOwnedData(deps, "user-b")
	seedForeignData(deps)

	if err := store.WithinTransaction(ctx, func(txCtx context.Context) error {
		return purger.PurgeForUser(txCtx, "user-a")
	}); err != nil {
		t.Fatalf("清理账号失败: %v", err)
	}
	if remaining, _ := purgeCoverage(deps, "user-a"); len(remaining) > 0 {
		t.Fatalf("账号数据未清理干净，仍残留 %v", remaining)
	}
	for _, other := range []string{"user-b", "user-a-extra"} {
		if _, missing := purgeCoverage(deps, other); len(missing) > 0 {
			t.Fatalf("误删相邻账号 %s，缺失 %v", other, missing)
		}
	}
}

/* TestAccountPurgerRestoresOnRollback 校验事务失败时按快照恢复全部集合 */
func TestAccountPurgerRestoresOnRollback(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	deps, purger := newPurgerFixture(store)
	seedOwnedData(deps, "user-a")

	err := store.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := purger.PurgeForUser(txCtx, "user-a"); err != nil {
			return err
		}
		return errPurgerRollback
	})
	if err != errPurgerRollback {
		t.Fatalf("期望事务回滚，实际 err=%v", err)
	}
	if _, missing := purgeCoverage(deps, "user-a"); len(missing) > 0 {
		t.Fatalf("回滚后账号数据应完整保留，缺失 %v", missing)
	}
}

/* errPurgerRollback 触发内存事务回滚，验证撤销日志生效 */
var errPurgerRollback = errors.New("purger rollback probe")
