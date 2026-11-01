/*
 * 标签模块的持久化端口。
 *
 * 职责：描述标签本体与「笔记-标签」关联的读写，隐藏索引、连接表与事务实现。
 * 不负责：重名裁决、标签上限与笔记状态校验（由用例与领域负责）。
 * 约束：所有方法必须携带 userID，跨用户访问一律按 RESOURCE_NOT_FOUND 处理（G-08）。
 * 参考：docs/API.md#57-标签、docs/ARCHITECTURE.md#55-数据模型-postgresql-16
 */
package port

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* TagOrder 是标签列表允许的排序方式，取值与契约一致 */
type TagOrder string

/* 排序白名单：笔记数倒序（默认）与名称升序 */
const (
	TagOrderNoteCountDesc TagOrder = "noteCount desc"
	TagOrderNameAsc       TagOrder = "name asc"
)

/*
 * TagSortKey 返回升序可比的排序键。
 * 笔记数倒序用「最大值减计数」翻转；名称使用小写形式，与数据库 lower(name) 唯一索引口径一致。
 */
func TagSortKey(order TagOrder, tag *entity.Tag, noteCount int) string {
	if order == TagOrderNameAsc {
		return strings.ToLower(tag.Name)
	}
	return fmt.Sprintf("%020d", math.MaxInt64-int64(noteCount))
}

/* TagCursor 是键集分页位置，排序键与 ID 共同保证翻页稳定 */
type TagCursor struct {
	SortKey string
	ID      string
}

/*
 * TagRepository 管理标签本体。
 * 约定：Create 遇到账号内重名返回 NAME_DUPLICATED；GetByID 返回未删除标签或 RESOURCE_NOT_FOUND。
 */
type TagRepository interface {
	Create(ctx context.Context, tag *entity.Tag) error
	GetByID(ctx context.Context, userID, tagID string) (*entity.Tag, error)
	List(ctx context.Context, userID string) ([]*entity.Tag, error)
	Update(ctx context.Context, tag *entity.Tag) error
	Delete(ctx context.Context, userID, tagID string) error
	DeleteAllByUser(ctx context.Context, userID string) error
}

/*
 * NoteTagRepository 管理笔记与标签的多对多关联。
 * 约定：ReplaceForNote 整体覆盖一篇笔记的标签；CountsByTags 只统计未删除笔记，
 * tagIDs 为空表示统计全部标签；NoteIDsByTags 的语义为「同时包含」全部给定标签。
 */
type NoteTagRepository interface {
	ReplaceForNote(ctx context.Context, userID, noteID string, tagIDs []string) error
	TagsByNotes(ctx context.Context, userID string, noteIDs []string) (map[string][]*entity.Tag, error)
	NoteIDsByTags(ctx context.Context, userID string, tagIDs []string) ([]string, error)
	CountsByTags(ctx context.Context, userID string, tagIDs []string) (map[string]int, error)
	DeleteByTag(ctx context.Context, userID, tagID string) error
	DeleteByNote(ctx context.Context, userID, noteID string) error
	DeleteAllByUser(ctx context.Context, userID string) error
}
