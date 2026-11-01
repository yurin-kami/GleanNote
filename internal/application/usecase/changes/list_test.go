/* 增量同步用例测试：游标推进、类型过滤与保留期过期 */
package changes

import (
	"context"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/infrastructure/memory"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* newTestService 装配使用真实内存变更日志的服务；时间固定为 2026-10-28T06:00:00Z */
func newTestService(t *testing.T) (*Service, *memory.ChangeLogRepository, *clock.Fixed) {
	t.Helper()

	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(now)
	store := memory.NewStore()
	log := memory.NewChangeLogRepository(store)
	store.SetChangeLog(log)
	service, err := NewService(Deps{Clock: fixed, Log: log})
	if err != nil {
		t.Fatalf("创建增量同步服务: %v", err)
	}
	return service, log, fixed
}

/* appendEvent 直接写入一条事件，绕开业务仓储以获得可控序号 */
func appendEvent(t *testing.T, log *memory.ChangeLogRepository, userID string,
	resource entity.ChangeResourceType, id string, op entity.ChangeOperation, at time.Time) *entity.ChangeEvent {
	t.Helper()

	event := &entity.ChangeEvent{
		UserID:       userID,
		ResourceType: resource,
		ResourceID:   id,
		Op:           op,
		Revision:     1,
		UpdatedAt:    at,
	}
	if op != entity.ChangeOpDelete {
		event.Payload = map[string]any{"title": "标题"}
	}
	if err := log.Append(context.Background(), event); err != nil {
		t.Fatalf("追加事件: %v", err)
	}
	return event
}

/* TestListChangesPagesAndFilters 覆盖游标推进、hasMore 与资源类型过滤 */
func TestListChangesPagesAndFilters(t *testing.T) {
	service, log, fixed := newTestService(t)
	now := fixed.Now()
	appendEvent(t, log, "user-1", entity.ChangeResourceFolder, "folder-1", entity.ChangeOpCreate, now)
	appendEvent(t, log, "user-1", entity.ChangeResourceNote, "note-1", entity.ChangeOpCreate, now)
	appendEvent(t, log, "user-1", entity.ChangeResourceNote, "note-1", entity.ChangeOpDelete, now)

	first, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: 0, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("第一页失败: %v", err)
	}
	if len(first.Changes) != 2 || first.NextCursor != 2 || !first.HasMore {
		t.Fatalf("第一页不符: %+v", first)
	}
	if first.Changes[1].Payload == nil {
		t.Fatal("CREATE 事件应携带载荷")
	}

	second, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: first.NextCursor, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("第二页失败: %v", err)
	}
	if len(second.Changes) != 1 || second.NextCursor != 3 || second.HasMore {
		t.Fatalf("第二页不符: %+v", second)
	}
	if second.Changes[0].Op != string(entity.ChangeOpDelete) || second.Changes[0].Payload != nil {
		t.Fatalf("删除事件应为墓碑: %+v", second.Changes[0])
	}

	filtered, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: 0, PageSize: 10, ResourceTypes: []string{"note"},
	})
	if err != nil {
		t.Fatalf("过滤失败: %v", err)
	}
	if len(filtered.Changes) != 2 {
		t.Fatalf("仅应返回 NOTE 事件: %+v", filtered)
	}

	empty, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: 3, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("游标之后应为空: %v", err)
	}
	if len(empty.Changes) != 0 || empty.NextCursor != 3 || empty.HasMore {
		t.Fatalf("空页游标应保持不变: %+v", empty)
	}
}

/* TestListChangesRejectsInvalidParams 覆盖参数校验 */
func TestListChangesRejectsInvalidParams(t *testing.T) {
	service, _, _ := newTestService(t)

	cases := []struct {
		name  string
		input ListChangesInput
	}{
		{"缺少用户", ListChangesInput{Since: 0}},
		{"负游标", ListChangesInput{UserID: "user-1", Since: -1}},
		{"分页过大", ListChangesInput{UserID: "user-1", PageSize: MaxPageSize + 1}},
		{"未知类型", ListChangesInput{UserID: "user-1", PageSize: 10, ResourceTypes: []string{"UNKNOWN"}}},
	}
	for _, item := range cases {
		if _, err := service.ListChanges(context.Background(), item.input); err == nil {
			t.Fatalf("%s: 期望校验错误", item.name)
		}
	}
}

/* TestListChangesExpiresPrunedCursor 覆盖 90 天保留期导致的游标过期 */
func TestListChangesExpiresPrunedCursor(t *testing.T) {
	service, log, fixed := newTestService(t)
	old := fixed.Now().Add(-(Retention + 24*time.Hour))
	appendEvent(t, log, "user-1", entity.ChangeResourceNote, "old-1", entity.ChangeOpCreate, old)
	appendEvent(t, log, "user-1", entity.ChangeResourceNote, "old-2", entity.ChangeOpCreate, old)
	appendEvent(t, log, "user-1", entity.ChangeResourceNote, "fresh", entity.ChangeOpCreate, fixed.Now())

	_, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: 1, PageSize: 10,
	})
	if !errs.IsCode(err, errs.CodeSyncCursorExpired) {
		t.Fatalf("期望 SYNC_CURSOR_EXPIRED，实际 %v", err)
	}

	/* 全量初始化（since=0）不受保留期影响 */
	result, err := service.ListChanges(context.Background(), ListChangesInput{
		UserID: "user-1", Since: 0, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("全量初始化失败: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].ResourceID != "fresh" {
		t.Fatalf("全量初始化应只返回未清理事件: %+v", result.Changes)
	}
}
