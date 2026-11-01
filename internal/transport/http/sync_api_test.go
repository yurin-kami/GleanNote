/*
 * 增量同步端点协议测试：游标、资源类型过滤与参数校验。
 * 契约来源：docs/API.md#510-增量同步、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

/* syncChangeBody 对应 openapi 的 SyncChange */
type syncChangeBody struct {
	Seq          int64           `json:"seq"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	Op           string          `json:"op"`
	Revision     int             `json:"revision"`
	UpdatedAt    string          `json:"updatedAt"`
	Payload      *map[string]any `json:"payload"`
}

/* syncBody 对应 openapi 的 SyncResponse */
type syncBody struct {
	Changes    []syncChangeBody `json:"changes"`
	NextCursor int64            `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
}

/* TestSyncChangesEndpoint 覆盖目录与笔记创建的增量拉取 */
func TestSyncChangesEndpoint(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "sync-api@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	note := createNoteHTTP(t, app, session.AccessToken, root, "同步", "正文", "sync-note-1")

	result := app.do(t, http.MethodGet, "/v1/sync:changes?pageSize=2", "", bearer(session.AccessToken))
	if result.status != http.StatusOK {
		t.Fatalf("同步状态码 = %d，原文 %q", result.status, string(result.body))
	}
	var body syncBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析同步响应失败: %v", err)
	}
	if len(body.Changes) != 2 || body.NextCursor != 2 || body.HasMore {
		t.Fatalf("第一页不符: %+v", body)
	}
	if body.Changes[0].ResourceType != "FOLDER" || body.Changes[1].ResourceType != "NOTE" {
		t.Fatalf("事件顺序不符: %+v", body.Changes)
	}

	/* 推进游标后再拉一页，应不再有新事件且游标保持不变 */
	cursor := body.NextCursor
	next := app.do(t, http.MethodGet,
		"/v1/sync:changes?pageSize=2&since="+strconv.FormatInt(cursor, 10), "", bearer(session.AccessToken))
	if next.status != http.StatusOK {
		t.Fatalf("下一页状态码 = %d", next.status)
	}
	if err := json.Unmarshal(next.body, &body); err != nil {
		t.Fatalf("解析下一页失败: %v", err)
	}
	if len(body.Changes) != 0 || body.NextCursor != cursor || body.HasMore {
		t.Fatalf("游标之后应为空: %+v", body)
	}

	/* 类型过滤只返回笔记事件 */
	filtered := app.do(t, http.MethodGet, "/v1/sync:changes?resourceTypes=NOTE", "",
		bearer(session.AccessToken))
	if filtered.status != http.StatusOK {
		t.Fatalf("过滤状态码 = %d", filtered.status)
	}
	var notes syncBody
	if err := json.Unmarshal(filtered.body, &notes); err != nil {
		t.Fatalf("解析过滤响应失败: %v", err)
	}
	if len(notes.Changes) < 1 || notes.Changes[0].ResourceID != note.ID {
		t.Fatalf("过滤结果不符: %+v", notes.Changes)
	}

	/* 未知资源类型返回 400 */
	invalid := app.do(t, http.MethodGet, "/v1/sync:changes?resourceTypes=UNKNOWN", "",
		bearer(session.AccessToken))
	if invalid.status != http.StatusBadRequest {
		t.Fatalf("未知类型状态码 = %d，原文 %q", invalid.status, string(invalid.body))
	}
}
