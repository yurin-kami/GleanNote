/*
 * 分享用例测试：开关状态、公开地址稳定性、公开读取与失效口径。
 * 契约来源：docs/API.md#59-分享、PRD FR-15。
 */
package share

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
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* testBaseURL 是夹具中的公开地址基址，断言时据此拼接分享链接 */
const testBaseURL = "https://gleannote.example"

/* fixture 汇总分享用例测试所需的依赖与共享存储 */
type fixture struct {
	service *Service
	clock   *clock.Fixed
	notes   *memory.NoteRepository
	folders *memory.FolderRepository
	pages   *memory.NotePageRepository
	shares  *memory.ShareLinkRepository
}

/* newFixture 装配分享服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	notes := memory.NewNoteRepository(store)
	folders := memory.NewFolderRepository(store)
	pages := memory.NewNotePageRepository(store)
	shares := memory.NewShareLinkRepository(store)
	service, err := NewService(Deps{
		Clock:   fixed,
		Notes:   notes,
		Folders: folders,
		Pages:   pages,
		Objects: objectstore.NewFake(fixed),
		Shares:  shares,
		BaseURL: testBaseURL + "/",
	})
	if err != nil {
		t.Fatalf("创建分享服务: %v", err)
	}
	return &fixture{service: service, clock: fixed, notes: notes, folders: folders, pages: pages, shares: shares}
}

/* seedRoot 写入账号根目录 */
func (f *fixture) seedRoot(t *testing.T, userID string) *entity.Folder {
	t.Helper()
	root := entity.NewRootFolder("root-"+userID, userID, f.clock.Now())
	if err := f.folders.Create(context.Background(), root); err != nil {
		t.Fatalf("写入根目录: %v", err)
	}
	return root
}

/* seedNote 写入一篇笔记；state=DRAFT 时构造草稿 */
func (f *fixture) seedNote(t *testing.T, noteID, userID, folderID, title, content, state string) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	var note *entity.Note
	if state == string(entity.NoteStateDraft) {
		note = entity.NewDraftNote(noteID, userID, folderID, parsedTitle, f.clock.Now())
	} else {
		note = entity.NewNote(noteID, userID, folderID, parsedTitle,
			valueobject.NoteContent(content), f.clock.Now(), f.clock.Now())
	}
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* seedPage 为笔记追加一页原图 */
func (f *fixture) seedPage(t *testing.T, noteID, userID string, index int) {
	t.Helper()
	uploadID := fmt.Sprintf("up-%s-%d", noteID, index)
	upload := entity.NewUpload(uploadID, userID, fmt.Sprintf("u/%s.jpg", uploadID), "p.jpg",
		"image/jpeg", strings.Repeat("a", 64), 2048, f.clock.Now().Add(time.Hour), f.clock.Now())
	upload.MarkReady(800, 600, fmt.Sprintf("thumb/%s.jpg", uploadID), strings.Repeat("a", 64))
	page := entity.NewNotePage(fmt.Sprintf("%s-page-%d", noteID, index), noteID, userID,
		index, upload, f.clock.Now())
	if err := f.pages.CreateBatch(context.Background(), []*entity.NotePage{page}); err != nil {
		t.Fatalf("写入页面: %v", err)
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

/* 开启分享返回稳定地址；关闭后旧地址立即失效 */
func TestEnableShareAndDisable(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	f.seedNote(t, "note-1", "user-1", root.ID, "矩阵的秩", "# 矩阵的秩\n正文", "NORMAL")
	ctx := context.Background()

	state, err := f.service.GetShareLink(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("读取初始分享状态: %v", err)
	}
	if state.Enabled || state.URL != "" {
		t.Fatalf("初始应未开启分享: %+v", state)
	}

	enabled, err := f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("开启分享: %v", err)
	}
	prefix := testBaseURL + "/s/"
	if !enabled.Enabled || !strings.HasPrefix(enabled.URL, prefix) {
		t.Fatalf("开启结果不符: %+v", enabled)
	}
	token := strings.TrimPrefix(enabled.URL, prefix)
	if len(token) != 43 {
		t.Fatalf("令牌长度 = %d，期望 43（32 字节无填充 Base64URL）", len(token))
	}

	/* 重复开启返回原地址，不重复生成令牌 */
	again, err := f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("重复开启分享: %v", err)
	}
	if again.URL != enabled.URL {
		t.Fatalf("重复开启地址变化: %q != %q", again.URL, enabled.URL)
	}

	if err := f.service.DisableShare(ctx, "user-1", "note-1"); err != nil {
		t.Fatalf("关闭分享: %v", err)
	}
	state, err = f.service.GetShareLink(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("关闭后读取分享状态: %v", err)
	}
	if state.Enabled || state.URL != "" {
		t.Fatalf("关闭后状态未清空: %+v", state)
	}

	/* 关闭后旧令牌按 410 失效；重复关闭保持幂等 */
	requireCode(t, errOf(func() error {
		_, err := f.service.PublicNote(ctx, token)
		return err
	}), errs.CodeShareLinkInactive)
	if err := f.service.DisableShare(ctx, "user-1", "note-1"); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
}

/* 跨账号操作他人笔记的分享一律 404，公开链接不受影响 */
func TestShareCrossAccountNotFound(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	f.seedNote(t, "note-1", "user-1", root.ID, "矩阵的秩", "# 矩阵的秩\n正文", "NORMAL")
	ctx := context.Background()

	enabled, err := f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("开启分享: %v", err)
	}

	requireCode(t, errOf(func() error {
		_, err := f.service.GetShareLink(ctx, "user-2", "note-1")
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		_, err := f.service.EnableShare(ctx, "user-2", "note-1")
		return err
	}), errs.CodeResourceNotFound)
	requireCode(t, errOf(func() error {
		return f.service.DisableShare(ctx, "user-2", "note-1")
	}), errs.CodeResourceNotFound)

	/* 越权尝试不得关闭他人分享，公开读取仍可用 */
	token := strings.TrimPrefix(enabled.URL, testBaseURL+"/s/")
	if _, err := f.service.PublicNote(ctx, token); err != nil {
		t.Fatalf("公开读取应仍然可用: %v", err)
	}
	state, err := f.service.GetShareLink(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("读取原账号分享状态: %v", err)
	}
	if !state.Enabled || state.URL != enabled.URL {
		t.Fatalf("越权操作影响了原账号分享: %+v", state)
	}
}

/* 仅 NORMAL 笔记可开启分享；未知笔记返回 404 */
func TestEnableShareRejectsDraft(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	f.seedNote(t, "note-draft", "user-1", root.ID, "草稿", "", "DRAFT")
	ctx := context.Background()

	requireCode(t, errOf(func() error {
		_, err := f.service.EnableShare(ctx, "user-1", "note-draft")
		return err
	}), errs.CodeShareNoteNotReady)
	requireCode(t, errOf(func() error {
		_, err := f.service.EnableShare(ctx, "user-1", "note-missing")
		return err
	}), errs.CodeResourceNotFound)
}

/* 公开读取返回标题、正文与页面原图地址，且不含目录与标签 */
func TestPublicNoteReturnsContentAndPages(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	f.seedNote(t, "note-1", "user-1", root.ID, "矩阵的秩", "# 矩阵的秩\n正文", "NORMAL")
	f.seedPage(t, "note-1", "user-1", 1)
	f.seedPage(t, "note-1", "user-1", 2)
	ctx := context.Background()

	enabled, err := f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("开启分享: %v", err)
	}
	token := strings.TrimPrefix(enabled.URL, testBaseURL+"/s/")

	view, err := f.service.PublicNote(ctx, "  "+token+"  ")
	if err != nil {
		t.Fatalf("公开读取: %v", err)
	}
	if view.Title != "矩阵的秩" || !strings.Contains(view.Content, "正文") {
		t.Fatalf("公开内容不符: %+v", view)
	}
	if len(view.Pages) != 2 || view.Pages[0].Index != 1 || view.Pages[0].OriginalURL == "" {
		t.Fatalf("公开页图片不符: %+v", view.Pages)
	}
	if view.UpdatedAt.IsZero() {
		t.Fatal("公开内容必须携带更新时间")
	}
}

/* 笔记删除、目录删除与未知令牌统一按 410 失效，且不泄露内容 */
func TestPublicNoteInvalidation(t *testing.T) {
	f := newFixture(t)
	root := f.seedRoot(t, "user-1")
	note := f.seedNote(t, "note-1", "user-1", root.ID, "标题", "正文", "NORMAL")
	ctx := context.Background()

	enabled, err := f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("开启分享: %v", err)
	}
	token := strings.TrimPrefix(enabled.URL, testBaseURL+"/s/")

	/* 笔记进入回收站后链接失效 */
	note.MarkDeleted("batch-1", f.clock.Now(), 30*24*time.Hour)
	if err := f.notes.Update(ctx, note); err != nil {
		t.Fatalf("删除笔记: %v", err)
	}
	requireCode(t, errOf(func() error {
		_, err := f.service.PublicNote(ctx, token)
		return err
	}), errs.CodeShareLinkInactive)

	/* 恢复笔记后重新开启；目录删除同样使链接失效 */
	note.Restore(root.ID, f.clock.Now())
	if err := f.notes.Update(ctx, note); err != nil {
		t.Fatalf("恢复笔记: %v", err)
	}
	enabled, err = f.service.EnableShare(ctx, "user-1", "note-1")
	if err != nil {
		t.Fatalf("重新开启分享: %v", err)
	}
	token = strings.TrimPrefix(enabled.URL, testBaseURL+"/s/")

	stored, err := f.folders.GetByID(ctx, "user-1", root.ID)
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	stored.MarkDeleted("batch-2", f.clock.Now(), 30*24*time.Hour)
	if err := f.folders.Update(ctx, stored); err != nil {
		t.Fatalf("删除目录: %v", err)
	}
	requireCode(t, errOf(func() error {
		_, err := f.service.PublicNote(ctx, token)
		return err
	}), errs.CodeShareLinkInactive)

	/* 未知令牌与空令牌同样按失效处理 */
	requireCode(t, errOf(func() error {
		_, err := f.service.PublicNote(ctx, "unknown-token")
		return err
	}), errs.CodeShareLinkInactive)
	requireCode(t, errOf(func() error {
		_, err := f.service.PublicNote(ctx, "   ")
		return err
	}), errs.CodeShareLinkInactive)
}

/* errOf 提取一次调用的错误，便于复用 requireCode 断言 */
func errOf(call func() error) error {
	return call()
}
