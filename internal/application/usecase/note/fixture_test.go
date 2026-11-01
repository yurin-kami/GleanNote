/* 笔记用例测试夹具：真实内存仓储 + 固定时钟 + 可预测 ID 与简化的目录恢复 */
package note

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

/* sequenceIDs 生成可读的自增 ID，便于断言与排查 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* fakeRestorer 只恢复目标目录本身，用于验证笔记恢复的目录裁决 */
type fakeRestorer struct {
	folders *memory.FolderRepository
	clock   *clock.Fixed
}

/* RestoreNoteFolder 把回收站目录原地恢复；父级仍删除时保持原路径 */
func (r *fakeRestorer) RestoreNoteFolder(ctx context.Context, userID, folderID string) (FolderRestore, error) {
	folder, err := r.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return FolderRestore{}, err
	}
	if !folder.IsDeleted() {
		return FolderRestore{FolderID: folder.ID}, nil
	}
	folder.Restore(folder.ParentID, folder.Depth, folder.Path, r.clock.Now())
	if err := r.folders.Update(ctx, folder); err != nil {
		return FolderRestore{}, err
	}
	return FolderRestore{FolderID: folder.ID}, nil
}

/* stubVersions 记录保存与删除时调用的版本动作，避免在笔记用例内依赖完整版本服务 */
type stubVersions struct {
	sources []string
	deleted []string
}

/* Record 记录版本来源并立即成功 */
func (s *stubVersions) Record(ctx context.Context, userID, noteID, source, content string) error {
	s.sources = append(s.sources, source)
	return nil
}

/* DeleteByNote 记录被清理的笔记并立即成功 */
func (s *stubVersions) DeleteByNote(ctx context.Context, userID, noteID string) error {
	s.deleted = append(s.deleted, noteID)
	return nil
}

/* fixture 汇总一次用例测试所需的依赖与共享存储 */
type fixture struct {
	service  *Service
	clock    *clock.Fixed
	store    *memory.Store
	folders  *memory.FolderRepository
	notes    *memory.NoteRepository
	tags     *memory.TagRepository
	links    *memory.NoteTagRepository
	uploads  *memory.UploadRepository
	pages    *memory.NotePageRepository
	jobs     *memory.ConversionJobRepository
	ledger   *memory.QuotaLedger
	usage    *memory.StorageUsage
	queue    *memory.ConversionQueue
	objects  *objectstore.Fake
	ids      *sequenceIDs
	shares   *memory.ShareLinkRepository
	versions *stubVersions
}

/* newFixture 装配笔记服务；时间固定为 2026-10-28T06:00:00Z 以便断言回收站到期 */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	folders := memory.NewFolderRepository(store)
	notes := memory.NewNoteRepository(store)
	tags := memory.NewTagRepository(store)
	links := memory.NewNoteTagRepository(store, tags, notes)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	ids := &sequenceIDs{next: 1000}
	uploads := memory.NewUploadRepository(store)
	pages := memory.NewNotePageRepository(store)
	jobs := memory.NewConversionJobRepository(store)
	ledger := memory.NewQuotaLedger(store)
	usage := memory.NewStorageUsage(store)
	queue := memory.NewConversionQueue(16)
	objects := objectstore.NewFake(fixed)
	shares := memory.NewShareLinkRepository(store)
	versions := &stubVersions{}
	service, err := NewService(Deps{
		Clock:          fixed,
		IDs:            ids,
		Notes:          notes,
		Folders:        folders,
		FolderRestorer: &fakeRestorer{folders: folders, clock: fixed},
		Uploads:        uploads,
		Pages:          pages,
		Jobs:           jobs,
		Quota:          ledger,
		Storage:        usage,
		Queue:          queue,
		Objects:        objects,
		TagLinks:       links,
		Versions:       versions,
		Shares:         shares,
		Transactions:   store,
		PageTokens:     codec,
	})
	if err != nil {
		t.Fatalf("创建笔记服务: %v", err)
	}
	return &fixture{
		service:  service,
		clock:    fixed,
		store:    store,
		folders:  folders,
		notes:    notes,
		tags:     tags,
		links:    links,
		uploads:  uploads,
		pages:    pages,
		jobs:     jobs,
		ledger:   ledger,
		usage:    usage,
		queue:    queue,
		objects:  objects,
		ids:      ids,
		shares:   shares,
		versions: versions,
	}
}

/* seedUpload 写入一次已完成校验的上传会话；ready=false 时保持 PENDING */
func (f *fixture) seedUpload(t *testing.T, uploadID, userID string, bytes int64, ready bool) *entity.Upload {
	t.Helper()
	upload := entity.NewUpload(uploadID, userID, "u/"+uploadID+".jpg", "p.jpg", "image/jpeg",
		strings.Repeat("a", 64), bytes, f.clock.Now().Add(time.Hour), f.clock.Now())
	if ready {
		upload.MarkReady(800, 600, "thumb/"+uploadID+".jpg", strings.Repeat("a", 64))
	}
	if err := f.uploads.CreateBatch(context.Background(), []*entity.Upload{upload}); err != nil {
		t.Fatalf("写入上传会话: %v", err)
	}
	return upload
}

/* seedRoot 写入账号根目录，返回根目录 ID */
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

/* create 调用创建用例并要求成功 */
func (f *fixture) create(t *testing.T, userID, folderID, title, content string) NoteView {
	t.Helper()
	view, err := f.service.CreateNote(context.Background(), CreateNoteInput{
		UserID:   userID,
		FolderID: folderID,
		Title:    title,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("创建笔记失败: %v", err)
	}
	return view
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

/* save 调用保存用例并要求成功，返回保存结果 */
func (f *fixture) save(t *testing.T, input UpdateNoteInput) SaveNoteResult {
	t.Helper()
	result, err := f.service.UpdateNote(context.Background(), input)
	if err != nil {
		t.Fatalf("保存笔记失败: %v", err)
	}
	return result
}

/* delete 调用删除用例并要求成功 */
func (f *fixture) delete(t *testing.T, userID, noteID string) DeleteNoteResult {
	t.Helper()
	result, err := f.service.DeleteNote(context.Background(), userID, noteID)
	if err != nil {
		t.Fatalf("删除笔记失败: %v", err)
	}
	return result
}
