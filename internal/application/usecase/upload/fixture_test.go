/* 上传用例测试夹具：内存仓储 + 内存对象存储 + 真实图片处理器 + 固定时钟 */
package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
	"github.com/gleannote/gleannote/internal/infrastructure/imageprocessor"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/infrastructure/objectstore"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* sequenceIDs 生成可读的自增 ID，便于断言 */
type sequenceIDs struct{ next int }

/* New 返回 id-0001 形式的确定性 ID */
func (g *sequenceIDs) New() (string, error) {
	g.next++
	return fmt.Sprintf("id-%04d", g.next), nil
}

/* fixture 汇总上传用例测试所需的依赖 */
type fixture struct {
	service *Service
	clock   *clock.Fixed
	store   *memory.Store
	uploads *memory.UploadRepository
	notes   *memory.NoteRepository
	pages   *memory.NotePageRepository
	objects *objectstore.Fake
	ids     *sequenceIDs
}

/* newFixture 装配上传服务；时间固定为 2026-10-28T06:00:00Z */
func newFixture(t *testing.T) *fixture {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	objects := objectstore.NewFake(fixed)
	uploads := memory.NewUploadRepository(store)
	notes := memory.NewNoteRepository(store)
	pages := memory.NewNotePageRepository(store)
	codec, err := NewPageTokenCodec([]byte(strings.Repeat("k", 32)), fixed)
	if err != nil {
		t.Fatalf("创建分页令牌编解码器: %v", err)
	}
	ids := &sequenceIDs{next: 1000}
	service, err := NewService(Deps{
		Clock:        fixed,
		IDs:          ids,
		Uploads:      uploads,
		Notes:        notes,
		Pages:        pages,
		Objects:      objects,
		Images:       imageprocessor.New(),
		Transactions: store,
		PageTokens:   codec,
	})
	if err != nil {
		t.Fatalf("创建上传服务: %v", err)
	}
	return &fixture{
		service: service,
		clock:   fixed,
		store:   store,
		uploads: uploads,
		notes:   notes,
		pages:   pages,
		objects: objects,
		ids:     ids,
	}
}

/* digest 返回字节的 SHA256 小写十六进制 */
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

/* createUpload 申请一个上传会话并要求成功，返回会话 ID */
func (f *fixture) createUpload(t *testing.T, userID, contentType string, data []byte) string {
	t.Helper()
	tickets, err := f.service.CreateUploads(context.Background(), userID, []UploadItem{{
		FileName:    "page.jpg",
		ContentType: contentType,
		Bytes:       int64(len(data)),
		SHA256:      digest(data),
	}})
	if err != nil {
		t.Fatalf("申请上传失败: %v", err)
	}
	return tickets[0].UploadID
}

/* seedNote 写入一篇笔记，供页面读取用例校验归属 */
func (f *fixture) seedNote(t *testing.T, noteID, userID string) {
	t.Helper()
	title, err := valueobject.ParseNoteTitle("")
	if err != nil {
		t.Fatalf("解析标题: %v", err)
	}
	content, err := valueobject.ParseNoteContent("")
	if err != nil {
		t.Fatalf("解析正文: %v", err)
	}
	note := entity.NewNote(noteID, userID, "folder-1", title, content, f.clock.Now(), f.clock.Now())
	if err := f.notes.Create(context.Background(), note); err != nil {
		t.Fatalf("写入笔记: %v", err)
	}
}
