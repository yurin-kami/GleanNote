/* 标签实体测试：构造与重命名的时间口径 */
package entity

import (
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* TestNewTagAndRename 覆盖构造初值与重命名后的更新时间 */
func TestNewTagAndRename(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	name, err := valueobject.ParseTagName("考研")
	if err != nil {
		t.Fatalf("解析标签名: %v", err)
	}
	tag := NewTag("tag-1", "user-1", name, now)
	if tag.ID != "tag-1" || tag.UserID != "user-1" || tag.Name != "考研" {
		t.Fatalf("构造结果不符合预期: %+v", tag)
	}
	if !tag.CreatedAt.Equal(now) || !tag.UpdatedAt.Equal(now) {
		t.Fatalf("创建时间与更新时间应为同一时刻: %+v", tag)
	}

	later := now.Add(time.Hour)
	renamed, err := valueobject.ParseTagName("数学")
	if err != nil {
		t.Fatalf("解析新标签名: %v", err)
	}
	tag.Rename(renamed, later)
	if tag.Name != "数学" {
		t.Fatalf("重命名后名称 = %q，期望 数学", tag.Name)
	}
	if !tag.UpdatedAt.Equal(later) || !tag.CreatedAt.Equal(now) {
		t.Fatalf("重命名只应刷新更新时间: %+v", tag)
	}
	if tag.TagName().Key() != "数学" {
		t.Fatalf("标签比较键不符合预期: %q", tag.TagName().Key())
	}
}
