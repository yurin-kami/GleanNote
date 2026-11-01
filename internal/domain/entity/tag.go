/* 标签实体：账号级共享标签与重命名 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/*
 * Tag 是账号级共享的标签。
 * 标签不归属目录，重命名后所有关联笔记同步展示新名称（docs/API.md#57-标签）。
 */
type Tag struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

/* NewTag 构造标签；名称已由值对象校验，创建时间与更新时间取同一时刻 */
func NewTag(id, userID string, name valueobject.TagName, now time.Time) *Tag {
	return &Tag{
		ID:        id,
		UserID:    userID,
		Name:      name.String(),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

/* Rename 覆盖标签名称并刷新更新时间；重名裁决由用例负责 */
func (t *Tag) Rename(name valueobject.TagName, now time.Time) {
	t.Name = name.String()
	t.UpdatedAt = now
}

/* TagName 返回标签名的值对象形式，便于比较键 */
func (t *Tag) TagName() valueobject.TagName { return valueobject.TagName(t.Name) }
