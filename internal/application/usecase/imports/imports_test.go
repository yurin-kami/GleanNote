/*
 * 批量导入用例测试：逐项状态、合并模式、上限校验与查询可见性。
 * 契约来源：docs/API.md#511-导出与导入、PRD FR-17。
 */
package imports_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	importsusecase "github.com/gleannote/gleannote/internal/application/usecase/imports"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* sequenceIDs 生成可读的自增 ID，便于断言导入任务标识 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/*
 * stubCreator 替代笔记用例，记录调用参数并按末位上传 ID 注入失败。
 * 导入用例只关心「调用是否发生、结果如何回填」，无需真实建笔记。
 */
type stubCreator struct {
	calls  []noteusecase.CreateFromPagesInput
	failOn map[string]error
}

/* CreateFromPages 按末位上传 ID 决定成功或失败 */
func (s *stubCreator) CreateFromPages(ctx context.Context,
	input noteusecase.CreateFromPagesInput) (noteusecase.CreateFromPagesResult, error) {
	s.calls = append(s.calls, input)
	last := input.UploadIDs[len(input.UploadIDs)-1]
	if err := s.failOn[last]; err != nil {
		return noteusecase.CreateFromPagesResult{}, err
	}
	return noteusecase.CreateFromPagesResult{
		Note: noteusecase.NoteView{ID: "note-" + strings.Join(input.UploadIDs, "-")},
	}, nil
}

/* fixture 汇总导入用例所需的目录仓储与任务仓储 */
type fixture struct {
	service *importsusecase.Service
	clock   *clock.Fixed
	folders *memory.FolderRepository
	jobs    *memory.ImportJobRepository
	creator *stubCreator
	root    *entity.Folder
}

/* newFixture 装配导入服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T, userID string) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	folders := memory.NewFolderRepository(store)
	root := entity.NewRootFolder("root-"+userID, userID, now)
	if err := folders.Create(context.Background(), root); err != nil {
		t.Fatalf("写入根目录: %v", err)
	}
	creator := &stubCreator{failOn: make(map[string]error)}
	jobs := memory.NewImportJobRepository(store)
	service, err := importsusecase.NewService(importsusecase.Deps{
		Clock:       fixed,
		IDs:         &sequenceIDs{},
		Folders:     folders,
		Jobs:        jobs,
		NoteCreator: creator,
	})
	if err != nil {
		t.Fatalf("创建导入服务: %v", err)
	}
	return &fixture{service: service, clock: fixed, folders: folders, jobs: jobs, creator: creator, root: root}
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

/* TestPerImageImport 校验逐张创建、顺序保持与逐项笔记标识 */
func TestPerImageImport(t *testing.T) {
	f := newFixture(t, "user-1")
	view, err := f.service.CreateImport(context.Background(), importsusecase.CreateImportInput{
		UserID:    "user-1",
		FolderID:  f.root.ID,
		Mode:      "per_image",
		UploadIDs: []string{"u1", "u2"},
	})
	if err != nil {
		t.Fatalf("发起导入: %v", err)
	}
	if view.State != string(entity.ImportStateSucceeded) || view.Total != 2 || view.Succeeded != 2 {
		t.Fatalf("导入视图 = %+v", view)
	}
	if len(f.creator.calls) != 2 {
		t.Fatalf("创建调用次数 = %d", len(f.creator.calls))
	}
	for index, item := range view.Items {
		if item.UploadID != []string{"u1", "u2"}[index] || item.State != string(entity.ImportItemSucceeded) {
			t.Fatalf("导入项 = %+v", item)
		}
		if item.NoteID != "note-u1" && item.NoteID != "note-u2" {
			t.Fatalf("导入项笔记标识 = %q", item.NoteID)
		}
		if len(f.creator.calls[index].UploadIDs) != 1 {
			t.Fatalf("逐张模式应单张提交，实际 %v", f.creator.calls[index].UploadIDs)
		}
	}
}

/* TestPerImagePartialFailure 校验单项失败不阻断其余项并落定 PARTIAL_FAILED */
func TestPerImagePartialFailure(t *testing.T) {
	f := newFixture(t, "user-1")
	f.creator.failOn["u2"] = errs.New(errs.CodeQuotaExceeded, "额度不足")
	view, err := f.service.CreateImport(context.Background(), importsusecase.CreateImportInput{
		UserID:    "user-1",
		FolderID:  f.root.ID,
		Mode:      "PER_IMAGE",
		UploadIDs: []string{"u1", "u2"},
	})
	if err != nil {
		t.Fatalf("发起导入: %v", err)
	}
	if view.State != string(entity.ImportStatePartialFailed) || view.Succeeded != 1 || view.Failed != 1 {
		t.Fatalf("导入视图 = %+v", view)
	}
	failed := view.Items[1]
	if failed.State != string(entity.ImportItemFailed) || failed.FailureCode != string(errs.CodeQuotaExceeded) ||
		failed.NoteID != "" {
		t.Fatalf("失败项 = %+v", failed)
	}
}

/* TestMergeImport 校验合并模式只提交一次且所有项共享同一笔记 */
func TestMergeImport(t *testing.T) {
	f := newFixture(t, "user-1")
	view, err := f.service.CreateImport(context.Background(), importsusecase.CreateImportInput{
		UserID:    "user-1",
		FolderID:  f.root.ID,
		Mode:      "MERGE",
		UploadIDs: []string{"u1", "u2", "u3"},
	})
	if err != nil {
		t.Fatalf("发起合并导入: %v", err)
	}
	if view.State != string(entity.ImportStateSucceeded) || view.Total != 3 || view.Succeeded != 3 {
		t.Fatalf("导入视图 = %+v", view)
	}
	if len(f.creator.calls) != 1 ||
		strings.Join(f.creator.calls[0].UploadIDs, ",") != "u1,u2,u3" {
		t.Fatalf("合并调用 = %+v", f.creator.calls)
	}
	for _, item := range view.Items {
		if item.NoteID != "note-u1-u2-u3" || item.State != string(entity.ImportItemSucceeded) {
			t.Fatalf("合并导入项 = %+v", item)
		}
	}
}

/* TestMergeImportFailure 校验合并失败时所有项都记录同一失败码 */
func TestMergeImportFailure(t *testing.T) {
	f := newFixture(t, "user-1")
	f.creator.failOn["u2"] = errs.New(errs.CodeUploadExpired, "上传会话已超时")
	view, err := f.service.CreateImport(context.Background(), importsusecase.CreateImportInput{
		UserID:    "user-1",
		FolderID:  f.root.ID,
		Mode:      "MERGE",
		UploadIDs: []string{"u1", "u2"},
	})
	if err != nil {
		t.Fatalf("发起合并导入: %v", err)
	}
	if view.State != string(entity.ImportStateFailed) || view.Succeeded != 0 || view.Failed != 2 {
		t.Fatalf("导入视图 = %+v", view)
	}
	for _, item := range view.Items {
		if item.State != string(entity.ImportItemFailed) ||
			item.FailureCode != string(errs.CodeUploadExpired) {
			t.Fatalf("失败项 = %+v", item)
		}
	}
}

/* TestImportValidation 校验入参、上限与目录可见性 */
func TestImportValidation(t *testing.T) {
	f := newFixture(t, "user-1")
	name, err := valueobject.ParseFolderName("回收站目录")
	if err != nil {
		t.Fatalf("解析目录名: %v", err)
	}
	deleted := entity.NewFolder("folder-deleted", "user-1", f.root, name, f.clock.Now())
	if err := f.folders.Create(context.Background(), deleted); err != nil {
		t.Fatalf("写入已删除目录: %v", err)
	}
	deletedAt := f.clock.Now()
	deleted.DeletedAt = &deletedAt
	if err := f.folders.Update(context.Background(), deleted); err != nil {
		t.Fatalf("标记目录已删除: %v", err)
	}

	tooMany := make([]string, importsusecase.MaxImportUploads+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("u%d", index)
	}
	mergeTooMany := tooMany[:importsusecase.MaxMergeUploads+1]

	valid := importsusecase.CreateImportInput{UserID: "user-1", FolderID: f.root.ID, Mode: "PER_IMAGE"}
	cases := []struct {
		name  string
		input importsusecase.CreateImportInput
		want  errs.Code
	}{
		{"缺少用户", importsusecase.CreateImportInput{
			FolderID: f.root.ID, Mode: "PER_IMAGE", UploadIDs: []string{"u1"}},
			errs.CodeValidationFailed},
		{"缺少目录", importsusecase.CreateImportInput{
			UserID: "user-1", Mode: "PER_IMAGE", UploadIDs: []string{"u1"}},
			errs.CodeValidationFailed},
		{"非法模式", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: f.root.ID, Mode: "SINGLE", UploadIDs: []string{"u1"}},
			errs.CodeValidationFailed},
		{"空上传清单", valid, errs.CodeValidationFailed},
		{"重复上传", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: f.root.ID, Mode: "PER_IMAGE", UploadIDs: []string{"u1", "u1"}},
			errs.CodeValidationFailed},
		{"超过 50 张", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: f.root.ID, Mode: "PER_IMAGE", UploadIDs: tooMany},
			errs.CodeValidationFailed},
		{"合并超过 20 张", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: f.root.ID, Mode: "MERGE", UploadIDs: mergeTooMany},
			errs.CodeValidationFailed},
		{"目录不存在", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: "folder-missing", Mode: "PER_IMAGE", UploadIDs: []string{"u1"}},
			errs.CodeResourceNotFound},
		{"目录已删除", importsusecase.CreateImportInput{
			UserID: "user-1", FolderID: deleted.ID, Mode: "PER_IMAGE", UploadIDs: []string{"u1"}},
			errs.CodeResourceNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := f.service.CreateImport(context.Background(), testCase.input)
			requireCode(t, err, testCase.want)
		})
	}
}

/* TestGetImport 校验任务查询回填逐项结果且跨用户不可见 */
func TestGetImport(t *testing.T) {
	f := newFixture(t, "user-1")
	created, err := f.service.CreateImport(context.Background(), importsusecase.CreateImportInput{
		UserID: "user-1", FolderID: f.root.ID, Mode: "PER_IMAGE", UploadIDs: []string{"u1"},
	})
	if err != nil {
		t.Fatalf("发起导入: %v", err)
	}

	view, err := f.service.GetImport(context.Background(), "user-1", created.ID)
	if err != nil {
		t.Fatalf("查询导入: %v", err)
	}
	if view.State != created.State || len(view.Items) != 1 || view.Items[0].NoteID != "note-u1" {
		t.Fatalf("查询视图 = %+v", view)
	}

	_, err = f.service.GetImport(context.Background(), "other-user", created.ID)
	requireCode(t, err, errs.CodeResourceNotFound)
}
