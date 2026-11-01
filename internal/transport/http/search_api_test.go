/*
 * 搜索端点的协议测试：关键词、标题优先、摘要标记与参数校验。
 * 契约来源：docs/API.md#5311-get-v1notessearch、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

/* searchItemBody 与 SearchResultList 的单项对齐 */
type searchItemBody struct {
	NoteID     string `json:"noteId"`
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	FolderPath string `json:"folderPath"`
	UpdatedAt  string `json:"updatedAt"`
}

/* searchListBody 是搜索集合响应 */
type searchListBody struct {
	Items         []searchItemBody `json:"items"`
	NextPageToken string           `json:"nextPageToken"`
	TotalSize     int              `json:"totalSize"`
}

/* decodeSearch 解析搜索结果 */
func decodeSearch(t *testing.T, result httpResult) searchListBody {
	t.Helper()
	var body searchListBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析搜索结果失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* TestSearchNotesEndpoint 校验标题命中优先、摘要与目录路径 */
func TestSearchNotesEndpoint(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "search-api@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	math := createFolderHTTP(t, app, session.AccessToken, root, "数学", "search-folder-key-1")

	titleHit := createNoteHTTP(t, app, session.AccessToken, math.ID, "矩阵的秩", "本文不含关键词", "search-note-key-1")
	contentHit := createNoteHTTP(t, app, session.AccessToken, math.ID, "线性代数", "矩阵的秩等于非零行数", "search-note-key-2")

	result := app.do(t, http.MethodGet, "/v1/notes:search?query="+url.QueryEscape("秩"),
		"", bearer(session.AccessToken))
	if result.status != http.StatusOK {
		t.Fatalf("搜索状态码 = %d，原文 %q", result.status, string(result.body))
	}
	body := decodeSearch(t, result)
	if body.TotalSize != 2 || len(body.Items) != 2 {
		t.Fatalf("命中数不符: %+v", body)
	}
	if body.Items[0].NoteID != titleHit.ID || body.Items[1].NoteID != contentHit.ID {
		t.Fatalf("标题命中未优先: %+v", body.Items)
	}
	if body.Items[0].FolderPath != "我的笔记 / 数学" || body.Items[0].UpdatedAt != "2026-10-28T06:00:00Z" {
		t.Fatalf("路径或时间不符: %+v", body.Items[0])
	}
	if body.Items[1].Snippet != "矩阵的<em>秩</em>等于非零行数" {
		t.Fatalf("摘要 = %q", body.Items[1].Snippet)
	}

	/* 目录过滤含子树；不在该子树的笔记不返回 */
	scoped := app.do(t, http.MethodGet,
		"/v1/notes:search?query="+url.QueryEscape("秩")+"&folderId="+url.QueryEscape(math.ID),
		"", bearer(session.AccessToken))
	if scoped.status != http.StatusOK || decodeSearch(t, scoped).TotalSize != 2 {
		t.Fatalf("目录过滤 = %d %q", scoped.status, string(scoped.body))
	}
}

/* TestSearchNotesValidation 校验 query 与分页参数 */
func TestSearchNotesValidation(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "search-invalid@example.com", "Password1")

	missing := app.do(t, http.MethodGet, "/v1/notes:search", "", bearer(session.AccessToken))
	if missing.status != http.StatusBadRequest || decodeProblem(t, missing).Code != "VALIDATION_FAILED" {
		t.Fatalf("缺少 query = %d %q", missing.status, string(missing.body))
	}
	badPage := app.do(t, http.MethodGet, "/v1/notes:search?query=a&pageSize=abc",
		"", bearer(session.AccessToken))
	if badPage.status != http.StatusBadRequest {
		t.Fatalf("非法 pageSize = %d %q", badPage.status, string(badPage.body))
	}
	unknownFolder := app.do(t, http.MethodGet, "/v1/notes:search?query=a&folderId=missing",
		"", bearer(session.AccessToken))
	if unknownFolder.status != http.StatusNotFound {
		t.Fatalf("未知目录 = %d %q", unknownFolder.status, string(unknownFolder.body))
	}
}
