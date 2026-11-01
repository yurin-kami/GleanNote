/*
 * 目录端点的协议测试：覆盖 9 个端点、冒号方法改写、幂等重放与 400/404/409/412 口径。
 * 契约来源：docs/API.md#52-目录、docs/openapi.yaml。
 */
package httptransport_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

/* folderBody 是目录响应的解析结构，字段与 openapi 的 Folder 对齐 */
type folderBody struct {
	ID             string  `json:"id"`
	ParentID       *string `json:"parentId"`
	Name           string  `json:"name"`
	Depth          int     `json:"depth"`
	Revision       int     `json:"revision"`
	ChildCount     int     `json:"childCount"`
	NoteCount      int     `json:"noteCount"`
	RestoredToRoot *bool   `json:"restoredToRoot"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

/* folderListBody 是集合响应，含分页令牌与总数 */
type folderListBody struct {
	Items         []folderBody `json:"items"`
	NextPageToken string       `json:"nextPageToken"`
	TotalSize     int          `json:"totalSize"`
}

/* decodeFolder 解析单个目录响应，失败即终止测试 */
func decodeFolder(t *testing.T, result httpResult) folderBody {
	t.Helper()
	var body folderBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析目录响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* decodeFolderList 解析目录集合响应 */
func decodeFolderList(t *testing.T, result httpResult) folderListBody {
	t.Helper()
	var body folderListBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析目录列表失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* rootFolderID 读取注册时自动创建的根目录标识 */
func rootFolderID(t *testing.T, app *testApp, token string) string {
	t.Helper()
	result := app.do(t, http.MethodGet, "/v1/folders", "", bearer(token))
	if result.status != http.StatusOK {
		t.Fatalf("读取根级列表状态码 = %d，原文 %q", result.status, string(result.body))
	}
	list := decodeFolderList(t, result)
	if list.TotalSize != 1 || len(list.Items) != 1 {
		t.Fatalf("根级应只有注册创建的根目录: %+v", list)
	}
	return list.Items[0].ID
}

/* createFolderHTTP 通过接口创建目录，要求 201 并返回解析后的对象 */
func createFolderHTTP(t *testing.T, app *testApp, token, parentID, name, key string) folderBody {
	t.Helper()
	payload := fmt.Sprintf(`{"parentId":%q,"name":%q}`, parentID, name)
	result := app.do(t, http.MethodPost, "/v1/folders", payload,
		map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": key})
	if result.status != http.StatusCreated {
		t.Fatalf("创建目录 %q 状态码 = %d，原文 %q", name, result.status, string(result.body))
	}
	return decodeFolder(t, result)
}

/* 创建、详情、面包屑、改名与修订号冲突构成完整生命周期 */
func TestFolderLifecycle(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "folder-life@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)

	createdPayload := fmt.Sprintf(`{"parentId":%q,"name":"数学"}`, root)
	headers := map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "folder-key-1"}
	first := app.do(t, http.MethodPost, "/v1/folders", createdPayload, headers)
	if first.status != http.StatusCreated {
		t.Fatalf("创建目录状态码 = %d，原文 %q", first.status, string(first.body))
	}
	created := decodeFolder(t, first)
	if created.Name != "数学" || created.Depth != 2 || created.ParentID == nil || *created.ParentID != root {
		t.Fatalf("创建结果不符: %+v", created)
	}
	if created.CreatedAt != "2026-10-28T06:00:00Z" || created.Revision != 1 {
		t.Fatalf("创建时间或修订号不符: %+v", created)
	}

	/* 同键同体必须回放首次结果，不重复创建 */
	replayed := app.do(t, http.MethodPost, "/v1/folders", createdPayload, headers)
	if replayed.status != http.StatusCreated || !bytes.Equal(first.body, replayed.body) {
		t.Fatalf("幂等重放不一致: %d %q", replayed.status, string(replayed.body))
	}

	detail := app.do(t, http.MethodGet, "/v1/folders/"+created.ID, "", bearer(session.AccessToken))
	if detail.status != http.StatusOK || decodeFolder(t, detail).ChildCount != 0 {
		t.Fatalf("详情响应 = %d %q", detail.status, string(detail.body))
	}

	path := app.do(t, http.MethodGet, "/v1/folders/"+created.ID+"/path", "", bearer(session.AccessToken))
	var breadcrumb struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
		DisplayPath string `json:"displayPath"`
	}
	if path.status != http.StatusOK {
		t.Fatalf("面包屑状态码 = %d，原文 %q", path.status, string(path.body))
	}
	if err := json.Unmarshal(path.body, &breadcrumb); err != nil {
		t.Fatalf("解析面包屑失败: %v", err)
	}
	if banner := breadcrumb.DisplayPath; banner != "我的笔记 / 数学" {
		t.Fatalf("展示路径 = %q，期望「我的笔记 / 数学」", banner)
	}

	renamed := app.do(t, http.MethodPatch, "/v1/folders/"+created.ID, `{"name":"线性代数","revision":1}`,
		bearer(session.AccessToken))
	if renamed.status != http.StatusOK {
		t.Fatalf("改名状态码 = %d，原文 %q", renamed.status, string(renamed.body))
	}
	if view := decodeFolder(t, renamed); view.Name != "线性代数" || view.Revision != 2 {
		t.Fatalf("改名结果不符: %+v", view)
	}

	stale := app.do(t, http.MethodPatch, "/v1/folders/"+created.ID, `{"name":"高等数学","revision":1}`,
		bearer(session.AccessToken))
	if stale.status != http.StatusPreconditionFailed || decodeProblem(t, stale).Code != "REVISION_MISMATCH" {
		t.Fatalf("旧修订号响应 = %d %q，期望 412 REVISION_MISMATCH", stale.status, string(stale.body))
	}
}

/* 集合接口按 pageSize 分页并给出令牌；非法 pageSize 与排序返回 400 */
func TestFolderListPaginationAndValidation(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "folder-list@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	for index, name := range []string{"甲", "乙", "丙"} {
		createFolderHTTP(t, app, session.AccessToken, root, name, fmt.Sprintf("folder-key-%d", index+1))
	}

	first := app.do(t, http.MethodGet, "/v1/folders?parentId="+root+"&pageSize=2", "", bearer(session.AccessToken))
	if first.status != http.StatusOK {
		t.Fatalf("首页状态码 = %d，原文 %q", first.status, string(first.body))
	}
	page := decodeFolderList(t, first)
	if len(page.Items) != 2 || page.TotalSize != 3 || page.NextPageToken == "" {
		t.Fatalf("首页结果不符: %+v", page)
	}

	second := app.do(t, http.MethodGet,
		"/v1/folders?parentId="+root+"&pageSize=2&pageToken="+page.NextPageToken, "", bearer(session.AccessToken))
	if second.status != http.StatusOK {
		t.Fatalf("第二页状态码 = %d，原文 %q", second.status, string(second.body))
	}
	if next := decodeFolderList(t, second); len(next.Items) != 1 || next.Items[0].Name != "丙" {
		t.Fatalf("第二页结果不符: %+v", next)
	}

	negative := app.do(t, http.MethodGet, "/v1/folders?pageSize=-1", "", bearer(session.AccessToken))
	if negative.status != http.StatusBadRequest || decodeProblem(t, negative).Code != "VALIDATION_FAILED" {
		t.Fatalf("负数 pageSize 响应 = %d %q", negative.status, string(negative.body))
	}

	order := app.do(t, http.MethodGet, "/v1/folders?orderBy=updatedAt%20desc", "", bearer(session.AccessToken))
	if order.status != http.StatusBadRequest || decodeProblem(t, order).Code != "VALIDATION_FAILED" {
		t.Fatalf("非法排序响应 = %d %q", order.status, string(order.body))
	}

	unauthorized := app.do(t, http.MethodGet, "/v1/folders", "", nil)
	if unauthorized.status != http.StatusUnauthorized || decodeProblem(t, unauthorized).Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("无令牌响应 = %d %q", unauthorized.status, string(unauthorized.body))
	}
}

/* 同级重名返回 409，未知目录返回 404，跨越账号访问同样 404 */
func TestFolderConflictAndNotFound(t *testing.T) {
	app := newTestApp(t)
	alice := app.registerSession(t, "folder-alice@example.com", "Password1")
	bob := app.registerSession(t, "folder-bob@example.com", "Password1")
	root := rootFolderID(t, app, alice.AccessToken)
	createFolderHTTP(t, app, alice.AccessToken, root, "资料", "folder-key-1")

	duplicate := app.do(t, http.MethodPost, "/v1/folders",
		fmt.Sprintf(`{"parentId":%q,"name":"资料"}`, root),
		map[string]string{"Authorization": "Bearer " + alice.AccessToken, "Idempotency-Key": "folder-key-2"})
	if duplicate.status != http.StatusConflict || decodeProblem(t, duplicate).Code != "NAME_DUPLICATED" {
		t.Fatalf("重名响应 = %d %q，期望 409 NAME_DUPLICATED", duplicate.status, string(duplicate.body))
	}

	missing := app.do(t, http.MethodGet, "/v1/folders/does-not-exist", "", bearer(alice.AccessToken))
	if missing.status != http.StatusNotFound || decodeProblem(t, missing).Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("未知目录响应 = %d %q", missing.status, string(missing.body))
	}

	crossUser := app.do(t, http.MethodGet, "/v1/folders/"+root, "", bearer(bob.AccessToken))
	if crossUser.status != http.StatusNotFound {
		t.Fatalf("跨账号读取状态码 = %d，期望 404", crossUser.status)
	}
}

/* 冒号方法：移动、恢复与彻底删除；删除整棵树后恢复子目录应回落到根级 */
func TestFolderColonMethods(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "folder-colon@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	target := createFolderHTTP(t, app, session.AccessToken, root, "目标", "folder-key-1")
	source := createFolderHTTP(t, app, session.AccessToken, root, "来源", "folder-key-2")

	moved := app.do(t, http.MethodPost, "/v1/folders/"+source.ID+":move",
		fmt.Sprintf(`{"targetParentId":%q}`, target.ID), bearer(session.AccessToken))
	if moved.status != http.StatusOK {
		t.Fatalf("移动状态码 = %d，原文 %q", moved.status, string(moved.body))
	}
	var movedBody struct {
		Folder           folderBody `json:"folder"`
		MovedFolderCount int        `json:"movedFolderCount"`
	}
	if err := json.Unmarshal(moved.body, &movedBody); err != nil {
		t.Fatalf("解析移动结果失败: %v", err)
	}
	if movedBody.Folder.ParentID == nil || *movedBody.Folder.ParentID != target.ID || movedBody.MovedFolderCount != 1 {
		t.Fatalf("移动结果不符: %+v", movedBody)
	}

	cycle := app.do(t, http.MethodPost, "/v1/folders/"+target.ID+":move",
		fmt.Sprintf(`{"targetParentId":%q}`, source.ID), bearer(session.AccessToken))
	if cycle.status != http.StatusConflict || decodeProblem(t, cycle).Code != "FOLDER_CYCLE" {
		t.Fatalf("环检测响应 = %d %q，期望 409 FOLDER_CYCLE", cycle.status, string(cycle.body))
	}

	removed := app.do(t, http.MethodDelete, "/v1/folders/"+root, "", bearer(session.AccessToken))
	if removed.status != http.StatusOK {
		t.Fatalf("删除状态码 = %d，原文 %q", removed.status, string(removed.body))
	}
	var deleteBody struct {
		DeleteBatchID string `json:"deleteBatchId"`
		FolderCount   int    `json:"folderCount"`
		PurgeAt       string `json:"purgeAt"`
	}
	if err := json.Unmarshal(removed.body, &deleteBody); err != nil {
		t.Fatalf("解析删除结果失败: %v", err)
	}
	if deleteBody.DeleteBatchID == "" || deleteBody.FolderCount != 3 || deleteBody.PurgeAt == "" {
		t.Fatalf("删除摘要不符: %+v", deleteBody)
	}

	hidden := app.do(t, http.MethodGet, "/v1/folders/"+source.ID, "", bearer(session.AccessToken))
	if hidden.status != http.StatusNotFound {
		t.Fatalf("回收站目录状态码 = %d，期望 404", hidden.status)
	}

	restored := app.do(t, http.MethodPost, "/v1/folders/"+source.ID+":undelete", "", bearer(session.AccessToken))
	if restored.status != http.StatusOK {
		t.Fatalf("恢复状态码 = %d，原文 %q", restored.status, string(restored.body))
	}
	view := decodeFolder(t, restored)
	if view.RestoredToRoot == nil || !*view.RestoredToRoot || view.ParentID != nil || view.Depth != 1 {
		t.Fatalf("应回落到根级: %+v", view)
	}

	/* 先恢复到回收站再彻底删除，验证 204 与后续 404 */
	if result := app.do(t, http.MethodDelete, "/v1/folders/"+source.ID, "", bearer(session.AccessToken)); result.status != http.StatusOK {
		t.Fatalf("再次删除状态码 = %d，原文 %q", result.status, string(result.body))
	}
	expunged := app.do(t, http.MethodPost, "/v1/folders/"+source.ID+":expunge", "", bearer(session.AccessToken))
	if expunged.status != http.StatusNoContent {
		t.Fatalf("彻底删除状态码 = %d，原文 %q", expunged.status, string(expunged.body))
	}
	gone := app.do(t, http.MethodGet, "/v1/folders/"+source.ID, "", bearer(session.AccessToken))
	if gone.status != http.StatusNotFound {
		t.Fatalf("彻底删除后状态码 = %d，期望 404", gone.status)
	}
}
