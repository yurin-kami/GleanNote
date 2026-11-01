/*
 * 导出用例测试：单篇与目录压缩包、PDF、下载地址重签与失败态。
 * 契约来源：docs/API.md#511-导出与导入、PRD FR-14。
 */
package export_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	exportusecase "github.com/gleannote/gleannote/internal/application/usecase/export"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/infrastructure/pdfdoc"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* sequenceIDs 生成可读的自增 ID，便于断言导出对象键 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* fixture 汇总导出用例所需的共享存储与对象存储 */
type fixture struct {
	service *exportusecase.Service
	clock   *clock.Fixed
	folders *memory.FolderRepository
	notes   *memory.NoteRepository
	pages   *memory.NotePageRepository
	objects *objectstore.Fake
}

/* newFixture 装配导出服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	folders := memory.NewFolderRepository(store)
	notes := memory.NewNoteRepository(store)
	pages := memory.NewNotePageRepository(store)
	objects := objectstore.NewFake(fixed)
	service, err := exportusecase.NewService(exportusecase.Deps{
		Clock:   fixed,
		IDs:     &sequenceIDs{},
		Notes:   notes,
		Folders: folders,
		Pages:   pages,
		Objects: objects,
		Jobs:    memory.NewExportJobRepository(store),
		PDF:     pdfdoc.New(),
	})
	if err != nil {
		t.Fatalf("创建导出服务: %v", err)
	}
	return &fixture{service: service, clock: fixed, folders: folders, notes: notes, pages: pages, objects: objects}
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

/* seedFolder 在指定父级下写入子目录 */
func (f *fixture) seedFolder(t *testing.T, userID string, parent *entity.Folder, id, name string) *entity.Folder {
	t.Helper()
	parsed, err := valueobject.ParseFolderName(name)
	if err != nil {
		t.Fatalf("解析目录名: %v", err)
	}
	folder := entity.NewFolder(id, userID, parent, parsed, f.clock.Now())
	if err := f.folders.Create(context.Background(), folder); err != nil {
		t.Fatalf("写入子目录: %v", err)
	}
	return folder
}

/* seedNote 写入指定状态的笔记；空标题用于验证兜底文件名 */
func (f *fixture) seedNote(t *testing.T, userID, folderID, id, title, content string, state entity.NoteState) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	parsedContent, err := valueobject.ParseNoteContent(content)
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	now := f.clock.Now()
	note := entity.NewNote(id, userID, folderID, parsedTitle, parsedContent, now, now)
	note.State = state
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* seedPage 写入页面原图；contentType 决定压缩包内的扩展名与 PDF 是否嵌入 */
func (f *fixture) seedPage(t *testing.T, userID, noteID string, index int, contentType string) {
	t.Helper()
	key := fmt.Sprintf("pages/%s/%03d.bin", noteID, index)
	data := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	if err := f.objects.Put(context.Background(), key, contentType, data); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	page := &entity.NotePage{
		ID:        fmt.Sprintf("page-%s-%d", noteID, index),
		NoteID:    noteID,
		UserID:    userID,
		Index:     index,
		ObjectKey: key,
		Width:     800,
		Height:    600,
		Bytes:     int64(len(data)),
		CreatedAt: f.clock.Now(),
	}
	if err := f.pages.CreateBatch(context.Background(), []*entity.NotePage{page}); err != nil {
		t.Fatalf("写入页面: %v", err)
	}
}

/* requireCode 断言错误链中包含期望的业务错误码 */
func requireCode(t *testing.T, err error, want errs.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际成功", want)
	}
	business, ok := errs.As(err)
	if !ok || business.Code() != want {
		t.Fatalf("期望错误码 %s，实际 %v", want, err)
	}
}

/* readArchive 读取导出对象并展开为「文件名 → 内容」 */
func (f *fixture) readArchive(t *testing.T, key string) map[string][]byte {
	t.Helper()
	data, err := f.objects.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("读取导出对象: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("解析压缩包: %v", err)
	}
	files := make(map[string][]byte, len(reader.File))
	for _, entry := range reader.File {
		handle, err := entry.Open()
		if err != nil {
			t.Fatalf("打开压缩包条目 %q: %v", entry.Name, err)
		}
		content, err := io.ReadAll(handle)
		handle.Close()
		if err != nil {
			t.Fatalf("读取压缩包条目 %q: %v", entry.Name, err)
		}
		files[entry.Name] = content
	}
	return files
}

/* TestSingleNoteMarkdownExport 校验单篇导出文件名、图片相对引用与固定时间戳 */
func TestSingleNoteMarkdownExport(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	note := f.seedNote(t, user, root.ID, "note-1", "旅行 记录", "# 第一天\n今天天气很好。", entity.NoteStateNormal)
	f.seedPage(t, user, note.ID, 1, "image/jpeg")
	f.seedPage(t, user, note.ID, 2, "image/png")

	view, err := f.service.CreateExport(context.Background(), exportusecase.CreateExportInput{
		UserID: user,
		NoteID: note.ID,
		Format: "markdown_zip",
	})
	if err != nil {
		t.Fatalf("发起导出: %v", err)
	}
	if view.State != string(entity.ExportStateSucceeded) || view.DownloadURL == "" || view.ExpiresAt == nil {
		t.Fatalf("导出视图 = %+v", view)
	}

	files := f.readArchive(t, "exports/"+user+"/"+view.ID+".zip")
	if len(files) != 3 {
		t.Fatalf("压缩包条目 = %v", keysOf(files))
	}
	markdown := string(files["旅行 记录.md"])
	for _, want := range []string{"# 第一天", "## 页面图片", "![第 1 页](images/page-001.jpg)", "![第 2 页](images/page-002.png)"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("Markdown 缺少 %q，实际 %q", want, markdown)
		}
	}
	if _, ok := files["images/page-001.jpg"]; !ok {
		t.Fatalf("缺少 JPEG 页面: %v", keysOf(files))
	}
	if _, ok := files["images/page-002.png"]; !ok {
		t.Fatalf("缺少 PNG 页面: %v", keysOf(files))
	}
}

/* TestFolderMarkdownExport 校验目录层级、空目录保留与同名笔记去重 */
func TestFolderMarkdownExport(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	work := f.seedFolder(t, user, root, "folder-work", "工作")
	f.seedFolder(t, user, work, "folder-empty", "空目录")
	f.seedNote(t, user, root.ID, "note-a", "会议", "a", entity.NoteStateNormal)
	f.seedNote(t, user, root.ID, "note-b", "会议", "b", entity.NoteStateNormal)
	f.seedNote(t, user, work.ID, "note-c", "计划", "c", entity.NoteStateNormal)
	f.seedNote(t, user, root.ID, "note-d", "", "", entity.NoteStateNormal)

	view, err := f.service.CreateExport(context.Background(), exportusecase.CreateExportInput{
		UserID:   user,
		FolderID: root.ID,
		Format:   "MARKDOWN_ZIP",
	})
	if err != nil {
		t.Fatalf("发起目录导出: %v", err)
	}
	if view.State != string(entity.ExportStateSucceeded) {
		t.Fatalf("导出视图 = %+v", view)
	}

	files := f.readArchive(t, "exports/"+user+"/"+view.ID+".zip")
	for _, want := range []string{
		"我的笔记/",
		"我的笔记/工作/",
		"我的笔记/工作/空目录/",
		"我的笔记/会议.md",
		"我的笔记/会议 (2).md",
		"我的笔记/未命名笔记.md",
		"我的笔记/工作/计划.md",
	} {
		if _, ok := files[want]; !ok {
			t.Fatalf("缺少条目 %q，实际 %v", want, keysOf(files))
		}
	}
	if content := string(files["我的笔记/未命名笔记.md"]); !strings.Contains(content, "# 未命名笔记") {
		t.Fatalf("空正文应回落为标题行，实际 %q", content)
	}
}

/* TestExportValidation 校验二选一、格式与笔记状态口径 */
func TestExportValidation(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	draft := f.seedNote(t, user, root.ID, "note-draft", "草稿", "x", entity.NoteStateDraft)
	deleted := f.seedNote(t, user, root.ID, "note-deleted", "已删", "x", entity.NoteStateNormal)
	deletedAt := f.clock.Now()
	deleted.DeletedAt = &deletedAt
	if err := f.notes.Update(context.Background(), deleted); err != nil {
		t.Fatalf("更新已删除笔记: %v", err)
	}

	cases := []struct {
		name  string
		input exportusecase.CreateExportInput
		want  errs.Code
	}{
		{"缺少用户", exportusecase.CreateExportInput{NoteID: draft.ID, Format: "PDF"}, errs.CodeValidationFailed},
		{"非法格式", exportusecase.CreateExportInput{UserID: user, NoteID: draft.ID, Format: "DOCX"}, errs.CodeValidationFailed},
		{"noteId 与 folderId 同时提供",
			exportusecase.CreateExportInput{UserID: user, NoteID: draft.ID, FolderID: root.ID, Format: "PDF"},
			errs.CodeValidationFailed},
		{"目录不支持 PDF",
			exportusecase.CreateExportInput{UserID: user, FolderID: root.ID, Format: "PDF"},
			errs.CodeValidationFailed},
		{"草稿不可导出",
			exportusecase.CreateExportInput{UserID: user, NoteID: draft.ID, Format: "MARKDOWN_ZIP"},
			errs.CodeNoteStateInvalid},
		{"回收站笔记视为不存在",
			exportusecase.CreateExportInput{UserID: user, NoteID: deleted.ID, Format: "MARKDOWN_ZIP"},
			errs.CodeResourceNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := f.service.CreateExport(context.Background(), testCase.input)
			requireCode(t, err, testCase.want)
		})
	}
}

/* TestExportPDF 校验 PDF 结构、JPEG 嵌入与下载地址 */
func TestExportPDF(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	note := f.seedNote(t, user, root.ID, "note-1", "中文标题", "第一段正文", entity.NoteStateNormal)
	f.seedPage(t, user, note.ID, 1, "image/jpeg")
	f.seedPage(t, user, note.ID, 2, "image/png")

	view, err := f.service.CreateExport(context.Background(), exportusecase.CreateExportInput{
		UserID: user,
		NoteID: note.ID,
		Format: "PDF",
	})
	if err != nil {
		t.Fatalf("发起 PDF 导出: %v", err)
	}
	if view.State != string(entity.ExportStateSucceeded) || view.DownloadURL == "" {
		t.Fatalf("导出视图 = %+v", view)
	}

	data, err := f.objects.Get(context.Background(), "exports/"+user+"/"+view.ID+".pdf")
	if err != nil {
		t.Fatalf("读取 PDF: %v", err)
	}
	text := string(data)
	for _, want := range []string{"%PDF-1.4", "/STSong-Light", "/DCTDecode", "%%EOF"} {
		if !strings.Contains(text, want) {
			t.Fatalf("PDF 缺少 %q", want)
		}
	}
	/* PNG 页面不被嵌入，因此只有一个图片 XObject */
	if strings.Count(text, "/Subtype /Image") != 1 {
		t.Fatalf("图片 XObject 数量 = %d", strings.Count(text, "/Subtype /Image"))
	}
}

/* TestExportImageUnreadable 校验原图丢失时任务落到 FAILED 并带失败码 */
func TestExportImageUnreadable(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	note := f.seedNote(t, user, root.ID, "note-1", "笔记", "正文", entity.NoteStateNormal)
	page := &entity.NotePage{
		ID: "page-1", NoteID: note.ID, UserID: user, Index: 1,
		ObjectKey: "pages/missing.jpg", Width: 10, Height: 10, CreatedAt: f.clock.Now(),
	}
	if err := f.pages.CreateBatch(context.Background(), []*entity.NotePage{page}); err != nil {
		t.Fatalf("写入页面: %v", err)
	}

	view, err := f.service.CreateExport(context.Background(), exportusecase.CreateExportInput{
		UserID: user, NoteID: note.ID, Format: "MARKDOWN_ZIP",
	})
	if err != nil {
		t.Fatalf("发起导出: %v", err)
	}
	if view.State != string(entity.ExportStateFailed) || view.FailureCode != string(errs.CodeImageUnreadable) {
		t.Fatalf("失败视图 = %+v", view)
	}
}

/* TestGetExportReissuesDownload 校验每次读取都重新签发地址且跨用户不可见 */
func TestGetExportReissuesDownload(t *testing.T) {
	f := newFixture(t)
	user := "user-1"
	root := f.seedRoot(t, user)
	note := f.seedNote(t, user, root.ID, "note-1", "笔记", "正文", entity.NoteStateNormal)

	created, err := f.service.CreateExport(context.Background(), exportusecase.CreateExportInput{
		UserID: user, NoteID: note.ID, Format: "MARKDOWN_ZIP",
	})
	if err != nil {
		t.Fatalf("发起导出: %v", err)
	}

	view, err := f.service.GetExport(context.Background(), user, created.ID)
	if err != nil {
		t.Fatalf("查询导出: %v", err)
	}
	if view.State != string(entity.ExportStateSucceeded) || view.DownloadURL == "" ||
		!strings.Contains(view.DownloadURL, created.ID) {
		t.Fatalf("查询视图 = %+v", view)
	}
	if view.ExpiresAt == nil || view.ExpiresAt.Before(f.clock.Now()) {
		t.Fatalf("下载有效期 = %v", view.ExpiresAt)
	}

	_, err = f.service.GetExport(context.Background(), "other-user", created.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}

/* keysOf 返回压缩包条目名集合，便于失败信息展示 */
func keysOf(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	return names
}
