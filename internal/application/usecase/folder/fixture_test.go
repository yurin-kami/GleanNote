/* 目录用例测试夹具：真实内存仓储 + 固定时钟 + 可预测 ID 与分页令牌 */
package folder

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* fakeObjects 只记录删除调用；目录测试不涉及对象存储的读写与签名 */
type fakeObjects struct{ deleted []string }

func (f *fakeObjects) Name() string { return "fake" }

func (f *fakeObjects) PresignPut(ctx context.Context, key, contentType string,
	ttl time.Duration) (port.PutTarget, error) {
	return port.PutTarget{}, nil
}

func (f *fakeObjects) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

func (f *fakeObjects) Stat(ctx context.Context, key string) (port.ObjectInfo, error) {
	return port.ObjectInfo{}, nil
}

func (f *fakeObjects) Put(ctx context.Context, key, contentType string, data []byte) error {
	return nil
}

func (f *fakeObjects) Get(ctx context.Context, key string) ([]byte, error) { return nil, nil }

func (f *fakeObjects) Delete(ctx context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeObjects) Health(ctx context.Context) error { return nil }

/* sequenceIDs 生成可读的自增 ID，便于断言与排查 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* fixture 汇总一次用例测试所需的依赖与共享存储 */
type fixture struct {
	service *Service
	clock   *clock.Fixed
	store   *memory.Store
	folders *memory.FolderRepository
	notes   *memory.NoteRepository
	ids     *sequenceIDs
}

/* newFixture 装配目录服务；时间固定为 2026-10-28T06:00:00Z 以便断言回收站到期 */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	folders := memory.NewFolderRepository(store)
	notes := memory.NewNoteRepository(store)
	tags := memory.NewTagRepository(store)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	ids := &sequenceIDs{next: 1000}
	service, err := NewService(Deps{
		Clock:        fixed,
		IDs:          ids,
		Folders:      folders,
		Notes:        notes,
		Pages:        memory.NewNotePageRepository(store),
		Jobs:         memory.NewConversionJobRepository(store),
		TagLinks:     memory.NewNoteTagRepository(store, tags, notes),
		Versions:     memory.NewNoteVersionRepository(store),
		Shares:       memory.NewShareLinkRepository(store),
		Storage:      memory.NewStorageUsage(store),
		Objects:      &fakeObjects{},
		Transactions: store,
		PageTokens:   codec,
	})
	if err != nil {
		t.Fatalf("创建目录服务: %v", err)
	}
	return &fixture{
		service: service,
		clock:   fixed,
		store:   store,
		folders: folders,
		notes:   notes,
		ids:     ids,
	}
}

/* seedRoot 直接写入账号根目录，绕开创建用例以便单独测试层级与重名 */
func (f *fixture) seedRoot(t *testing.T, userID string) *entity.Folder {
	t.Helper()
	root := entity.NewRootFolder("root-"+userID, userID, f.clock.Now())
	if err := f.folders.Create(context.Background(), root); err != nil {
		t.Fatalf("写入根目录: %v", err)
	}
	return root
}

/* create 调用创建用例并要求成功，返回新目录视图 */
func (f *fixture) create(t *testing.T, userID, parentID, name string) FolderView {
	t.Helper()
	view, err := f.service.CreateFolder(context.Background(), CreateFolderInput{
		UserID:   userID,
		ParentID: parentID,
		Name:     name,
	})
	if err != nil {
		t.Fatalf("创建目录 %q 失败: %v", name, err)
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

/* currentView 读取目录详情，用于断言持久化后的字段 */
func (f *fixture) currentView(t *testing.T, userID, folderID string) FolderView {
	t.Helper()
	view, err := f.service.GetFolder(context.Background(), userID, folderID)
	if err != nil {
		t.Fatalf("读取目录 %s 失败: %v", folderID, err)
	}
	return view
}

/* listQuery 是分页列表的简写，减少测试噪声 */
func (f *fixture) listQuery(t *testing.T, input ListFoldersInput) ListFoldersResult {
	t.Helper()
	result, err := f.service.ListFolders(context.Background(), input)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	return result
}
