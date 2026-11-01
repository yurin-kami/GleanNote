/*
 * 笔记端点的协议测试：8 个端点、冲突副本、回收站往返与参数口径。
 * 契约来源：docs/API.md#53-笔记、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

/* noteBody 与 openapi 的 NoteDetail 对齐；列表项缺少的字段解析时为零值 */
type noteBody struct {
	ID             string `json:"id"`
	FolderID       string `json:"folderId"`
	FolderPath     string `json:"folderPath"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Revision       int    `json:"revision"`
	CharCount      int    `json:"charCount"`
	PageCount      int    `json:"pageCount"`
	ThumbnailURL   string `json:"thumbnailUrl"`
	IsConflictCopy bool   `json:"isConflictCopy"`
	Content        string `json:"content"`
	RestoredToRoot *bool  `json:"restoredToRoot"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

/* noteListBody 是 AIP-158 集合响应 */
type noteListBody struct {
	Items         []noteBody `json:"items"`
	NextPageToken string     `json:"nextPageToken"`
	TotalSize     int        `json:"totalSize"`
}

/* decodeNote 解析单篇笔记响应，失败即终止测试 */
func decodeNote(t *testing.T, result httpResult) noteBody {
	t.Helper()
	var body noteBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析笔记响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* decodeNoteList 解析笔记集合响应 */
func decodeNoteList(t *testing.T, result httpResult) noteListBody {
	t.Helper()
	var body noteListBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析笔记列表失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* createNoteHTTP 通过接口创建笔记，要求 201 并返回解析后的详情 */
func createNoteHTTP(t *testing.T, app *testApp, token, folderID, title, content, key string) noteBody {
	t.Helper()
	payload := fmt.Sprintf(`{"folderId":%q,"title":%q,"content":%q}`, folderID, title, content)
	result := app.do(t, http.MethodPost, "/v1/notes", payload,
		map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": key})
	if result.status != http.StatusCreated {
		t.Fatalf("创建笔记 %q 状态码 = %d，原文 %q", title, result.status, string(result.body))
	}
	return decodeNote(t, result)
}

/* 创建、读取、保存、冲突副本、移动与回收站往返构成完整生命周期 */
func TestNoteLifecycle(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "note-life@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)

	created := createNoteHTTP(t, app, session.AccessToken, root, "数学", "hello", "note-key-1")
	if created.State != "NORMAL" || created.Revision != 1 || created.CharCount != 5 {
		t.Fatalf("创建结果不符: %+v", created)
	}
	if created.FolderPath != "我的笔记" || created.CreatedAt != "2026-10-28T06:00:00Z" {
		t.Fatalf("路径或时间不符: %+v", created)
	}

	detail := app.do(t, http.MethodGet, "/v1/notes/"+created.ID, "", bearer(session.AccessToken))
	if detail.status != http.StatusOK || decodeNote(t, detail).Content != "hello" {
		t.Fatalf("详情响应 = %d %q", detail.status, string(detail.body))
	}

	/* 目录详情应回填该目录的未删除笔记数 */
	folderDetail := app.do(t, http.MethodGet, "/v1/folders/"+root, "", bearer(session.AccessToken))
	if folderDetail.status != http.StatusOK || decodeFolder(t, folderDetail).NoteCount != 1 {
		t.Fatalf("目录 noteCount 不符: %d %q", folderDetail.status, string(folderDetail.body))
	}

	/* 保存推进修订号，字符数按 Unicode 码点统计 */
	saved := app.do(t, http.MethodPatch, "/v1/notes/"+created.ID,
		`{"title":"数学笔记","content":"你好","revision":1,"saveSource":"MANUAL_SAVE"}`,
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "note-save-key-1"})
	if saved.status != http.StatusOK {
		t.Fatalf("保存状态码 = %d，原文 %q", saved.status, string(saved.body))
	}
	var saveBody struct {
		Note           noteBody `json:"note"`
		ConflictCopyID string   `json:"conflictCopyId"`
	}
	if err := json.Unmarshal(saved.body, &saveBody); err != nil {
		t.Fatalf("解析保存结果失败: %v", err)
	}
	if saveBody.Note.Revision != 2 || saveBody.Note.CharCount != 2 || saveBody.ConflictCopyID != "" {
		t.Fatalf("保存结果不符: %+v", saveBody)
	}

	/* 旧修订号不返回 412，而是把当前正文另存为冲突副本 */
	stale := app.do(t, http.MethodPatch, "/v1/notes/"+created.ID,
		`{"content":"离线内容","revision":1}`,
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "note-save-key-2"})
	if stale.status != http.StatusOK {
		t.Fatalf("冲突保存状态码 = %d，原文 %q", stale.status, string(stale.body))
	}
	if err := json.Unmarshal(stale.body, &saveBody); err != nil {
		t.Fatalf("解析冲突结果失败: %v", err)
	}
	if saveBody.ConflictCopyID == "" || saveBody.Note.Revision != 3 || saveBody.Note.Content != "离线内容" {
		t.Fatalf("冲突副本结果不符: %+v", saveBody)
	}

	list := decodeNoteList(t, app.do(t, http.MethodGet,
		"/v1/notes?folderId="+root, "", bearer(session.AccessToken)))
	if list.TotalSize != 2 || len(list.Items) != 2 {
		t.Fatalf("冲突后目录内应有 2 篇: %+v", list)
	}
	copyTitle := ""
	for _, item := range list.Items {
		if item.IsConflictCopy {
			copyTitle = item.Title
		}
	}
	if !strings.Contains(copyTitle, "冲突副本") {
		t.Fatalf("冲突副本标题 = %q，期望含「冲突副本」", copyTitle)
	}

	target := createFolderHTTP(t, app, session.AccessToken, root, "目标", "note-folder-key")
	moved := app.do(t, http.MethodPost, "/v1/notes/"+created.ID+":move",
		fmt.Sprintf(`{"folderId":%q}`, target.ID), bearer(session.AccessToken))
	if moved.status != http.StatusOK {
		t.Fatalf("移动状态码 = %d，原文 %q", moved.status, string(moved.body))
	}
	if view := decodeNote(t, moved); view.FolderID != target.ID || view.Revision != 3 {
		t.Fatalf("移动结果不符: %+v", view)
	}

	deleted := app.do(t, http.MethodDelete, "/v1/notes/"+created.ID, "", bearer(session.AccessToken))
	if deleted.status != http.StatusOK {
		t.Fatalf("删除状态码 = %d，原文 %q", deleted.status, string(deleted.body))
	}
	var deleteBody struct {
		DeleteBatchID string `json:"deleteBatchId"`
		PurgeAt       string `json:"purgeAt"`
	}
	if err := json.Unmarshal(deleted.body, &deleteBody); err != nil {
		t.Fatalf("解析删除结果失败: %v", err)
	}
	if deleteBody.DeleteBatchID == "" || deleteBody.PurgeAt != "2026-11-27T06:00:00Z" {
		t.Fatalf("删除摘要不符: %+v", deleteBody)
	}

	trashed := app.do(t, http.MethodGet, "/v1/notes/"+created.ID, "", bearer(session.AccessToken))
	if trashed.status != http.StatusOK || decodeNote(t, trashed).State != "DELETED" {
		t.Fatalf("回收站详情 = %d %q", trashed.status, string(trashed.body))
	}

	restored := app.do(t, http.MethodPost, "/v1/notes/"+created.ID+":undelete", "", bearer(session.AccessToken))
	if restored.status != http.StatusOK {
		t.Fatalf("恢复状态码 = %d，原文 %q", restored.status, string(restored.body))
	}
	recovered := decodeNote(t, restored)
	if recovered.State != "NORMAL" || recovered.FolderID != target.ID ||
		recovered.RestoredToRoot == nil || *recovered.RestoredToRoot {
		t.Fatalf("恢复结果不符: %+v", recovered)
	}

	/* 彻底删除只允许回收站笔记：先再次删除，再 expunge */
	if result := app.do(t, http.MethodDelete, "/v1/notes/"+created.ID, "", bearer(session.AccessToken)); result.status != http.StatusOK {
		t.Fatalf("再次删除状态码 = %d，原文 %q", result.status, string(result.body))
	}
	expunged := app.do(t, http.MethodPost, "/v1/notes/"+created.ID+":expunge", "", bearer(session.AccessToken))
	if expunged.status != http.StatusNoContent {
		t.Fatalf("彻底删除状态码 = %d，原文 %q", expunged.status, string(expunged.body))
	}
	if gone := app.do(t, http.MethodGet, "/v1/notes/"+created.ID, "", bearer(session.AccessToken)); gone.status != http.StatusNotFound {
		t.Fatalf("彻底删除后状态码 = %d，期望 404", gone.status)
	}
}

/* 集合按更新时间倒序分页；非法参数、跨账号访问与状态前置条件按契约返回 */
func TestNoteListPaginationAndValidation(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "note-list@example.com", "Password1")
	other := app.registerSession(t, "note-other@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)

	for index, title := range []string{"甲", "乙", "丙"} {
		createNoteHTTP(t, app, session.AccessToken, root, title, "正文", fmt.Sprintf("note-key-%d", index+1))
	}

	first := decodeNoteList(t, app.do(t, http.MethodGet,
		"/v1/notes?folderId="+root+"&pageSize=2", "", bearer(session.AccessToken)))
	if len(first.Items) != 2 || first.TotalSize != 3 || first.NextPageToken == "" {
		t.Fatalf("首页结果不符: %+v", first)
	}
	second := decodeNoteList(t, app.do(t, http.MethodGet,
		"/v1/notes?folderId="+root+"&pageSize=2&pageToken="+first.NextPageToken, "", bearer(session.AccessToken)))
	if len(second.Items) != 1 || second.Items[0].Title != "丙" {
		t.Fatalf("第二页结果不符: %+v", second)
	}

	/* 参数口径：缺文件夹、负数页大小、非法排序、非法状态一律 400 */
	cases := []string{
		"/v1/notes",
		"/v1/notes?folderId=" + root + "&pageSize=-1",
		"/v1/notes?folderId=" + root + "&orderBy=updatedAt%20desc",
		"/v1/notes?folderId=" + root + "&state=ARCHIVED",
		"/v1/notes?folderId=" + root + "&updatedAfter=yesterday",
	}
	for _, path := range cases {
		result := app.do(t, http.MethodGet, path, "", bearer(session.AccessToken))
		if result.status != http.StatusBadRequest || decodeProblem(t, result).Code != "VALIDATION_FAILED" {
			t.Fatalf("%s 响应 = %d %q，期望 400 VALIDATION_FAILED", path, result.status, string(result.body))
		}
	}

	unauthorized := app.do(t, http.MethodGet, "/v1/notes?folderId="+root, "", nil)
	if unauthorized.status != http.StatusUnauthorized || decodeProblem(t, unauthorized).Code != "AUTH_TOKEN_INVALID" {
		t.Fatalf("无令牌响应 = %d %q", unauthorized.status, string(unauthorized.body))
	}

	missingFolder := app.do(t, http.MethodPost, "/v1/notes",
		`{"folderId":"does-not-exist","title":"孤立"}`,
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "note-key-missing"})
	if missingFolder.status != http.StatusNotFound {
		t.Fatalf("未知目录建笔记状态码 = %d，期望 404", missingFolder.status)
	}

	created := createNoteHTTP(t, app, session.AccessToken, root, "校验", "", "note-key-extra")
	invalid := []string{
		`{"content":"x","revision":0}`,
		`{"content":"x","revision":1,"saveSource":"UNKNOWN"}`,
		`{"title":"` + strings.Repeat("字", 101) + `","revision":1}`,
	}
	for index, payload := range invalid {
		result := app.do(t, http.MethodPatch, "/v1/notes/"+created.ID, payload,
			map[string]string{"Authorization": "Bearer " + session.AccessToken,
				"Idempotency-Key": fmt.Sprintf("note-bad-key-%d", index)})
		if result.status != http.StatusBadRequest || decodeProblem(t, result).Code != "VALIDATION_FAILED" {
			t.Fatalf("非法保存载荷 %s 响应 = %d %q", payload, result.status, string(result.body))
		}
	}

	notTrashed := app.do(t, http.MethodPost, "/v1/notes/"+created.ID+":expunge", "", bearer(session.AccessToken))
	if notTrashed.status != http.StatusConflict || decodeProblem(t, notTrashed).Code != "PRECONDITION_FAILED" {
		t.Fatalf("未删除笔记彻底删除响应 = %d %q", notTrashed.status, string(notTrashed.body))
	}

	crossUser := app.do(t, http.MethodGet, "/v1/notes/"+created.ID, "", bearer(other.AccessToken))
	if crossUser.status != http.StatusNotFound {
		t.Fatalf("跨账号读取状态码 = %d，期望 404", crossUser.status)
	}
}

/* 彻底删除目录应级联清理子树内笔记，恢复与再次彻底删除均返回 404 */
func TestFolderExpungeCascadesNotes(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "note-root@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	folder := createFolderHTTP(t, app, session.AccessToken, root, "临时", "note-root-key")

	created := createNoteHTTP(t, app, session.AccessToken, folder.ID, "临时笔记", "正文", "note-root-note")
	if result := app.do(t, http.MethodDelete, "/v1/notes/"+created.ID, "", bearer(session.AccessToken)); result.status != http.StatusOK {
		t.Fatalf("删除笔记状态码 = %d，原文 %q", result.status, string(result.body))
	}
	if result := app.do(t, http.MethodDelete, "/v1/folders/"+folder.ID, "", bearer(session.AccessToken)); result.status != http.StatusOK {
		t.Fatalf("删除目录状态码 = %d，原文 %q", result.status, string(result.body))
	}
	if result := app.do(t, http.MethodPost, "/v1/folders/"+folder.ID+":expunge", "", bearer(session.AccessToken)); result.status != http.StatusNoContent {
		t.Fatalf("彻底删除目录状态码 = %d，原文 %q", result.status, string(result.body))
	}

	/* 子树笔记随目录一并彻底删除，不再存在可恢复的回收站记录 */
	restored := app.do(t, http.MethodPost, "/v1/notes/"+created.ID+":undelete", "", bearer(session.AccessToken))
	if restored.status != http.StatusNotFound || decodeProblem(t, restored).Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("级联删除后恢复笔记响应 = %d %q", restored.status, string(restored.body))
	}
	if result := app.do(t, http.MethodGet, "/v1/notes/"+created.ID, "", bearer(session.AccessToken)); result.status != http.StatusNotFound {
		t.Fatalf("级联删除后读取笔记状态码 = %d，期望 404", result.status)
	}
	if result := app.do(t, http.MethodPost, "/v1/folders/"+folder.ID+":undelete", "", bearer(session.AccessToken)); result.status != http.StatusNotFound {
		t.Fatalf("级联删除后恢复目录状态码 = %d，期望 404", result.status)
	}
}
