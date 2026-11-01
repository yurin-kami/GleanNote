/*
 * 版本与分享端点的协议测试：生成、分页、详情、恢复、开关与匿名公开读取。
 * 契约来源：docs/API.md#58-版本、docs/API.md#59-分享、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

/* versionBody 与 openapi 的 Version 对齐 */
type versionBody struct {
	VersionID string `json:"versionId"`
	Source    string `json:"source"`
	CharCount int    `json:"charCount"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

/* versionListBody 是 AIP-158 集合响应 */
type versionListBody struct {
	Items         []versionBody `json:"items"`
	NextPageToken string        `json:"nextPageToken"`
}

/* shareLinkBody 与 openapi 的 ShareLink 对齐 */
type shareLinkBody struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

/* publicShareBody 是匿名读取响应 */
type publicShareBody struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Pages   []struct {
		Index       int    `json:"index"`
		OriginalURL string `json:"originalUrl"`
	} `json:"pages"`
	UpdatedAt string `json:"updatedAt"`
}

/* noteShareBody 在笔记详情上补充分享状态字段 */
type noteShareBody struct {
	noteBody
	ShareEnabled bool `json:"shareEnabled"`
}

/* decodeVersionList 解析版本集合响应 */
func decodeVersionList(t *testing.T, result httpResult) versionListBody {
	t.Helper()
	var body versionListBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析版本列表失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* decodeVersion 解析版本详情响应 */
func decodeVersion(t *testing.T, result httpResult) versionBody {
	t.Helper()
	var body versionBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析版本详情失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* decodeShareLink 解析分享状态响应 */
func decodeShareLink(t *testing.T, result httpResult) shareLinkBody {
	t.Helper()
	var body shareLinkBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析分享状态失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* saveNote 通过 PATCH 保存正文，要求成功 */
func saveNote(t *testing.T, app *testApp, token, noteID, payload, key string) httpResult {
	t.Helper()
	result := app.do(t, http.MethodPatch, "/v1/notes/"+noteID, payload,
		map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": key})
	if result.status != http.StatusOK {
		t.Fatalf("保存笔记状态码 = %d，原文 %q", result.status, string(result.body))
	}
	return result
}

/* 保存生成版本，版本端点提供分页列表、正文与恢复 */
func TestVersionEndpoints(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "version-api@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	note := createNoteHTTP(t, app, session.AccessToken, root, "数学", "初始正文", "version-note-1")

	/* 两次手动保存各落一条版本，内容分别为「第二版」与「第三版」 */
	saveNote(t, app, session.AccessToken, note.ID,
		`{"title":"数学","content":"第二版","revision":1,"saveSource":"MANUAL_SAVE"}`, "version-save-1")
	saveNote(t, app, session.AccessToken, note.ID,
		`{"title":"数学","content":"第三版","revision":2,"saveSource":"MANUAL_SAVE"}`, "version-save-2")

	first := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/versions?pageSize=1", "",
		bearer(session.AccessToken))
	if first.status != http.StatusOK {
		t.Fatalf("版本列表状态码 = %d，原文 %q", first.status, string(first.body))
	}
	page := decodeVersionList(t, first)
	if len(page.Items) != 1 || page.Items[0].Source != "MANUAL_SAVE" {
		t.Fatalf("第一页版本不符: %+v", page)
	}
	if page.NextPageToken == "" {
		t.Fatal("第一页应返回 nextPageToken")
	}
	newest := page.Items[0]

	second := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/versions?pageSize=1&pageToken="+page.NextPageToken, "",
		bearer(session.AccessToken))
	if second.status != http.StatusOK {
		t.Fatalf("第二页状态码 = %d，原文 %q", second.status, string(second.body))
	}
	nextPage := decodeVersionList(t, second)
	if len(nextPage.Items) != 1 || nextPage.Items[0].VersionID == newest.VersionID {
		t.Fatalf("第二页版本不符: %+v", nextPage)
	}
	older := nextPage.Items[0]

	/* 详情带正文：最新版本是第二次保存的内容 */
	detail := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/versions/"+newest.VersionID, "",
		bearer(session.AccessToken))
	if detail.status != http.StatusOK || decodeVersion(t, detail).Content != "第三版" {
		t.Fatalf("版本详情 = %d %q", detail.status, string(detail.body))
	}
	olderDetail := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/versions/"+older.VersionID, "",
		bearer(session.AccessToken))
	if olderDetail.status != http.StatusOK || decodeVersion(t, olderDetail).Content != "第二版" {
		t.Fatalf("较早版本详情 = %d %q", olderDetail.status, string(olderDetail.body))
	}

	/* 恢复较早版本：正文回到「第二版」，并生成 RESTORE 版本 */
	restored := app.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/versions/"+older.VersionID+":restore", "",
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "version-restore-1"})
	if restored.status != http.StatusOK {
		t.Fatalf("恢复版本状态码 = %d，原文 %q", restored.status, string(restored.body))
	}
	var restoredNote noteShareBody
	if err := json.Unmarshal(restored.body, &restoredNote); err != nil {
		t.Fatalf("解析恢复结果失败: %v", err)
	}
	if restoredNote.Content != "第二版" || restoredNote.Revision != 4 {
		t.Fatalf("恢复结果不符: %+v", restoredNote)
	}

	after := decodeVersionList(t, app.do(t, http.MethodGet,
		"/v1/notes/"+note.ID+"/versions", "", bearer(session.AccessToken)))
	if len(after.Items) != 3 || after.Items[0].Source != "RESTORE" {
		t.Fatalf("恢复后版本列表不符: %+v", after.Items)
	}
}

/* 分享开关与匿名读取：开启后详情回填 shareEnabled，关闭后公开地址立即失效 */
func TestShareEndpoints(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "share-api@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	note := createNoteHTTP(t, app, session.AccessToken, root, "矩阵的秩", "# 矩阵的秩\n正文", "share-note-1")

	initial := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/shareLink", "", bearer(session.AccessToken))
	if initial.status != http.StatusOK || decodeShareLink(t, initial).Enabled {
		t.Fatalf("初始分享状态 = %d %q", initial.status, string(initial.body))
	}

	enabled := app.do(t, http.MethodPost, "/v1/notes/"+note.ID+"/shareLink", "",
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "share-key-1"})
	if enabled.status != http.StatusCreated {
		t.Fatalf("开启分享状态码 = %d，原文 %q", enabled.status, string(enabled.body))
	}
	link := decodeShareLink(t, enabled)
	prefix := "https://gleannote.example/s/"
	if !link.Enabled || !strings.HasPrefix(link.URL, prefix) {
		t.Fatalf("分享地址不符: %+v", link)
	}
	token := strings.TrimPrefix(link.URL, prefix)

	/* 笔记详情回填分享状态 */
	detailResult := app.do(t, http.MethodGet, "/v1/notes/"+note.ID, "", bearer(session.AccessToken))
	var detail noteShareBody
	if err := json.Unmarshal(detailResult.body, &detail); err != nil {
		t.Fatalf("解析笔记详情失败: %v", err)
	}
	if !detail.ShareEnabled {
		t.Fatal("开启分享后详情 shareEnabled 应为 true")
	}

	/* 匿名读取不需要鉴权 */
	public := app.do(t, http.MethodGet, "/v1/public/shares/"+token, "", nil)
	if public.status != http.StatusOK {
		t.Fatalf("公开读取状态码 = %d，原文 %q", public.status, string(public.body))
	}
	/* 撤销需立即生效：公开读取不得被中间缓存 */
	if cache := public.header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("公开读取 Cache-Control = %q，期望 no-store", cache)
	}
	var published publicShareBody
	if err := json.Unmarshal(public.body, &published); err != nil {
		t.Fatalf("解析公开内容失败: %v", err)
	}
	if published.Title != "矩阵的秩" || !strings.Contains(published.Content, "正文") {
		t.Fatalf("公开内容不符: %+v", published)
	}

	/* 服务端渲染的公开页与分享地址 {base}/s/{token} 完全一致，且不输出任何脚本 */
	page := app.do(t, http.MethodGet, "/s/"+token, "", nil)
	if page.status != http.StatusOK || !strings.HasPrefix(page.contentType, "text/html") {
		t.Fatalf("公开页 = %d %q，期望 200 text/html", page.status, string(page.body))
	}
	if cache := page.header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("公开页 Cache-Control = %q，期望 no-store", cache)
	}
	if !strings.Contains(string(page.body), "矩阵的秩") || strings.Contains(string(page.body), "<script") {
		t.Fatalf("公开页内容不符: %q", string(page.body))
	}

	closed := app.do(t, http.MethodDelete, "/v1/notes/"+note.ID+"/shareLink", "",
		bearer(session.AccessToken))
	if closed.status != http.StatusNoContent {
		t.Fatalf("关闭分享状态码 = %d，原文 %q", closed.status, string(closed.body))
	}

	gone := app.do(t, http.MethodGet, "/v1/public/shares/"+token, "", nil)
	problem := decodeProblem(t, gone)
	if gone.status != http.StatusGone || problem.Code != "SHARE_LINK_INACTIVE" {
		t.Fatalf("失效后公开读取 = %d %+v", gone.status, problem)
	}
	if gonePage := app.do(t, http.MethodGet, "/s/"+token, "", nil); gonePage.status != http.StatusGone {
		t.Fatalf("失效后公开页状态码 = %d，期望 410", gonePage.status)
	}

	state := decodeShareLink(t, app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/shareLink", "",
		bearer(session.AccessToken)))
	if state.Enabled || state.URL != "" {
		t.Fatalf("关闭后分享状态未清空: %+v", state)
	}
}
