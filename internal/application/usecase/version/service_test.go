/*
 * 版本用例测试：生成与节流、列表分页与过期过滤、详情与恢复。
 * 契约来源：docs/API.md#58-版本、PRD FR-16。
 */
package version

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* sequenceIDs 生成可读的自增 ID，便于断言版本先后顺序 */
type sequenceIDs struct{ next int }

/* New 返回 ver-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("ver-%04d", g.next), nil
}

/* fixture 汇总版本用例测试所需的依赖与共享存储 */
type fixture struct {
	service  *Service
	clock    *clock.Fixed
	notes    *memory.NoteRepository
	versions *memory.NoteVersionRepository
}

/* newFixture 装配版本服务；时间固定为 2026-10-28T06:00:00Z 以便断言保留期 */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	notes := memory.NewNoteRepository(store)
	versions := memory.NewNoteVersionRepository(store)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	service, err := NewService(Deps{
		Clock:        fixed,
		IDs:          &sequenceIDs{next: 100},
		Versions:     versions,
		Notes:        notes,
		Transactions: store,
		PageTokens:   codec,
	})
	if err != nil {
		t.Fatalf("创建版本服务: %v", err)
	}
	return &fixture{service: service, clock: fixed, notes: notes, versions: versions}
}

/* seedNote 写入一篇 NORMAL 笔记 */
func (f *fixture) seedNote(t *testing.T, noteID, userID, title, content string) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	note := entity.NewNote(noteID, userID, "folder-1", parsedTitle,
		valueobject.NoteContent(content), f.clock.Now(), f.clock.Now())
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* seedVersion 直接写入一条快照，便于构造顺序与过期场景 */
func (f *fixture) seedVersion(t *testing.T, id, userID, noteID string,
	source entity.VersionSource, content string, at time.Time) {
	t.Helper()
	version := entity.NewNoteVersion(id, userID, noteID, source,
		valueobject.NoteContent(content), at)
	if err := f.versions.Create(context.Background(), version); err != nil {
		t.Fatalf("写入版本: %v", err)
	}
}

/* requireCode 断言错误链中包含期望的业务错误码 */
func requireCode(t *testing.T, err error, want errs.Code) *errs.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际成功", want)
	}
	business, ok := errs.As(err)
	if !ok || business.Code() != want {
		t.Fatalf("期望错误码 %s，实际 %v", want, err)
	}
	return business
}

/* 手动保存与自动保存都会生成版本，自动保存按 5 分钟节流 */
func TestRecordSourcesAndAutoSaveThrottle(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "标题", "当前正文")
	ctx := context.Background()

	if err := f.service.Record(ctx, "user-1", "note-1", "MANUAL_SAVE", "第一次"); err != nil {
		t.Fatalf("记录手动保存版本: %v", err)
	}
	if err := f.service.Record(ctx, "user-1", "note-1", "AUTO_SAVE", "第二次"); err != nil {
		t.Fatalf("记录自动保存版本: %v", err)
	}
	list, err := f.service.ListVersions(ctx, "user-1", "note-1", 0, "")
	if err != nil {
		t.Fatalf("读取版本列表: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(list.Items))
	}

	/* 同一次自动保存窗口内的第二次写入被节流丢弃 */
	if err := f.service.Record(ctx, "user-1", "note-1", "AUTO_SAVE", "第三次"); err != nil {
		t.Fatalf("记录被节流的自动保存: %v", err)
	}
	list, err = f.service.ListVersions(ctx, "user-1", "note-1", 0, "")
	if err != nil {
		t.Fatalf("读取版本列表: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("节流后版本数 = %d，期望仍为 2", len(list.Items))
	}

	/* 越过 5 分钟窗口后自动保存重新落库，且最新一条来源为 AUTO_SAVE */
	f.clock.Advance(entity.AutoVersionMinInterval + time.Minute)
	if err := f.service.Record(ctx, "user-1", "note-1", "AUTO_SAVE", "第四次"); err != nil {
		t.Fatalf("记录窗口外自动保存: %v", err)
	}
	list, err = f.service.ListVersions(ctx, "user-1", "note-1", 0, "")
	if err != nil {
		t.Fatalf("读取版本列表: %v", err)
	}
	if len(list.Items) != 3 || list.Items[0].Source != string(entity.VersionSourceAutoSave) {
		t.Fatalf("窗口外自动保存未生成版本: %+v", list.Items)
	}

	requireCode(t, f.service.Record(ctx, "user-1", "note-1", "RESTORE", "非法来源"), errs.CodeValidationFailed)
	requireCode(t, f.service.Record(ctx, "", "note-1", "MANUAL_SAVE", "缺少用户"), errs.CodeAuthTokenInvalid)
}

/* 列表按时间倒序分页，过期版本不出现；游标与笔记绑定 */
func TestListVersionsPaginationAndExpiry(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "标题", "正文")
	now := f.clock.Now()

	f.seedVersion(t, "ver-new", "user-1", "note-1", entity.VersionSourceManualSave, "最新", now.Add(-time.Hour))
	f.seedVersion(t, "ver-mid", "user-1", "note-1", entity.VersionSourceAutoSave, "中间", now.Add(-2*time.Hour))
	f.seedVersion(t, "ver-old", "user-1", "note-1", entity.VersionSourceConversion, "最早", now.Add(-3*time.Hour))
	f.seedVersion(t, "ver-expired", "user-1", "note-1", entity.VersionSourceManualSave,
		"已过期", now.Add(-entity.VersionRetention-time.Hour))

	first, err := f.service.ListVersions(context.Background(), "user-1", "note-1", 2, "")
	if err != nil {
		t.Fatalf("读取第一页: %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "ver-new" || first.Items[1].ID != "ver-mid" {
		t.Fatalf("第一页顺序不符: %+v", first.Items)
	}
	if first.NextPageToken == "" {
		t.Fatal("第一页应签发下一页令牌")
	}

	second, err := f.service.ListVersions(context.Background(), "user-1", "note-1", 2, first.NextPageToken)
	if err != nil {
		t.Fatalf("读取第二页: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "ver-old" {
		t.Fatalf("第二页结果不符: %+v", second.Items)
	}
	if second.NextPageToken != "" {
		t.Fatalf("末页不应再签发令牌: %q", second.NextPageToken)
	}

	requireCode(t, errOf(func() error {
		_, err := f.service.ListVersions(context.Background(), "user-1", "note-1", 20, "bogus")
		return err
	}), errs.CodeInvalidPageToken)
	requireCode(t, errOf(func() error {
		_, err := f.service.ListVersions(context.Background(), "user-1", "note-missing", 0, "")
		return err
	}), errs.CodeResourceNotFound)
}

/* 详情返回正文；过期版本按 404 处理 */
func TestGetVersionDetailAndExpired(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "标题", "正文")
	now := f.clock.Now()
	f.seedVersion(t, "ver-new", "user-1", "note-1", entity.VersionSourceManualSave, "历史正文", now.Add(-time.Hour))
	f.seedVersion(t, "ver-expired", "user-1", "note-1", entity.VersionSourceManualSave,
		"过期正文", now.Add(-entity.VersionRetention-time.Hour))

	detail, err := f.service.GetVersion(context.Background(), "user-1", "note-1", "ver-new")
	if err != nil {
		t.Fatalf("读取版本详情: %v", err)
	}
	if detail.Content != "历史正文" || detail.CharCount != 4 {
		t.Fatalf("版本详情不符: %+v", detail)
	}

	requireCode(t, errOf(func() error {
		_, err := f.service.GetVersion(context.Background(), "user-1", "note-1", "ver-expired")
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		_, err := f.service.GetVersion(context.Background(), "user-1", "note-1", "ver-missing")
		return err
	}), errs.CodeResourceNotFound)
}

/* 恢复只替换正文并生成 RESTORE 版本，不删除历史 */
func TestRestoreVersionReplacesContent(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "原标题", "当前正文")
	now := f.clock.Now()
	f.seedVersion(t, "ver-old", "user-1", "note-1", entity.VersionSourceManualSave, "历史正文", now.Add(-time.Hour))

	noteID, err := f.service.RestoreVersion(context.Background(), "user-1", "note-1", "ver-old")
	if err != nil {
		t.Fatalf("恢复版本: %v", err)
	}
	if noteID != "note-1" {
		t.Fatalf("返回笔记 ID = %q", noteID)
	}
	note, err := f.notes.GetByID(context.Background(), "user-1", "note-1")
	if err != nil {
		t.Fatalf("读取笔记: %v", err)
	}
	if note.Content != "历史正文" || note.Title != "原标题" || note.Revision != 2 {
		t.Fatalf("恢复结果不符: content=%q title=%q revision=%d", note.Content, note.Title, note.Revision)
	}

	list, err := f.service.ListVersions(context.Background(), "user-1", "note-1", 0, "")
	if err != nil {
		t.Fatalf("读取版本列表: %v", err)
	}
	if len(list.Items) != 2 || list.Items[0].Source != string(entity.VersionSourceRestore) {
		t.Fatalf("恢复后版本列表不符: %+v", list.Items)
	}
	if list.Items[0].CharCount != 4 {
		t.Fatalf("RESTORE 版本字数 = %d，期望 4", list.Items[0].CharCount)
	}

	/* 过期版本不可恢复，且不修改正文 */
	f.seedVersion(t, "ver-expired", "user-1", "note-1", entity.VersionSourceManualSave,
		"过期正文", now.Add(-entity.VersionRetention-time.Hour))
	requireCode(t, errOf(func() error {
		_, err := f.service.RestoreVersion(context.Background(), "user-1", "note-1", "ver-expired")
		return err
	}), errs.CodeResourceNotFound)
	note, err = f.notes.GetByID(context.Background(), "user-1", "note-1")
	if err != nil {
		t.Fatalf("读取笔记: %v", err)
	}
	if note.Content != "历史正文" {
		t.Fatalf("过期版本不应修改正文: %q", note.Content)
	}
}

/* 跨账号访问一律按资源不存在处理，避免泄露他人笔记是否存在 */
func TestVersionCrossAccountNotFound(t *testing.T) {
	f := newFixture(t)
	f.seedNote(t, "note-1", "user-1", "标题", "正文")
	f.seedVersion(t, "ver-1", "user-1", "note-1", entity.VersionSourceManualSave,
		"历史正文", f.clock.Now().Add(-time.Hour))
	ctx := context.Background()

	requireCode(t, errOf(func() error {
		_, err := f.service.ListVersions(ctx, "user-2", "note-1", 0, "")
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		_, err := f.service.GetVersion(ctx, "user-2", "note-1", "ver-1")
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		_, err := f.service.RestoreVersion(ctx, "user-2", "note-1", "ver-1")
		return err
	}), errs.CodeResourceNotFound)

	/* 原账号数据未被越权操作影响 */
	list, err := f.service.ListVersions(ctx, "user-1", "note-1", 0, "")
	if err != nil {
		t.Fatalf("读取原账号版本列表: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("原账号版本数量 = %d，期望 1", len(list.Items))
	}
}

/* errOf 提取一次调用的错误，便于复用 requireCode 断言 */
func errOf(call func() error) error {
	return call()
}
