/* 标签用例测试夹具：真实内存仓储 + 固定时钟 + 可预测 ID */
package tag

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
	tags    *memory.TagRepository
	links   *memory.NoteTagRepository
	notes   *memory.NoteRepository
	ids     *sequenceIDs
}

/* newFixture 装配标签服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	tags := memory.NewTagRepository(store)
	notes := memory.NewNoteRepository(store)
	links := memory.NewNoteTagRepository(store, tags, notes)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	ids := &sequenceIDs{next: 100}
	service, err := NewService(Deps{
		Clock:        fixed,
		IDs:          ids,
		Tags:         tags,
		NoteTags:     links,
		Notes:        notes,
		Transactions: store,
		PageTokens:   codec,
	})
	if err != nil {
		t.Fatalf("创建标签服务: %v", err)
	}
	return &fixture{
		service: service,
		clock:   fixed,
		store:   store,
		tags:    tags,
		links:   links,
		notes:   notes,
		ids:     ids,
	}
}

/* seedNote 写入一篇笔记，供标签设置断言 */
func (f *fixture) seedNote(t *testing.T, noteID, userID, title string) *entity.Note {
	t.Helper()
	parsedTitle, err := valueobject.ParseNoteTitle(title)
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	content, err := valueobject.ParseNoteContent("正文")
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	note := entity.NewNote(noteID, userID, "folder-1", parsedTitle, content, f.clock.Now(), f.clock.Now())
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
	return note
}

/* createTag 调用创建用例并要求成功 */
func (f *fixture) createTag(t *testing.T, userID, name string) TagView {
	t.Helper()
	view, err := f.service.CreateTag(context.Background(), CreateTagInput{UserID: userID, Name: name})
	if err != nil {
		t.Fatalf("创建标签失败: %v", err)
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
