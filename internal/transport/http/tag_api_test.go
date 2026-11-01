/*
 * 标签端点的协议测试：标签 CRUD、笔记标签覆盖、摘要回填与错误码。
 * 契约来源：docs/API.md#57-标签、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

/* tagBody 与 openapi 的 Tag 对齐 */
type tagBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	NoteCount int    `json:"noteCount"`
	CreatedAt string `json:"createdAt"`
}

/* tagListBody 是标签集合响应 */
type tagListBody struct {
	Items         []tagBody `json:"items"`
	NextPageToken string    `json:"nextPageToken"`
}

/* noteTagsBody 是笔记详情中内嵌的标签列表 */
type noteTagsBody struct {
	Tags []tagBody `json:"tags"`
}

/* decodeTag 解析单个标签响应 */
func decodeTag(t *testing.T, result httpResult) tagBody {
	t.Helper()
	var body tagBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析标签响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* createTagHTTP 通过接口新建标签，要求 201 */
func createTagHTTP(t *testing.T, app *testApp, token, name, key string) tagBody {
	t.Helper()
	result := app.do(t, http.MethodPost, "/v1/tags", fmt.Sprintf(`{"name":%q}`, name),
		map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": key})
	if result.status != http.StatusCreated {
		t.Fatalf("创建标签 %q 状态码 = %d，原文 %q", name, result.status, string(result.body))
	}
	return decodeTag(t, result)
}

/* TestTagLifecycle 覆盖新建、列表、笔记标签覆盖、重命名与删除 */
func TestTagLifecycle(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "tag-life@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)

	created := createTagHTTP(t, app, session.AccessToken, "考研", "tag-key-1")
	if created.Name != "考研" || created.NoteCount != 0 || created.CreatedAt != "2026-10-28T06:00:00Z" {
		t.Fatalf("新建标签结果不符: %+v", created)
	}

	/* 账号内重名（大小写不敏感）返回 409 */
	duplicate := app.do(t, http.MethodPost, "/v1/tags", `{"name":"考研"}`,
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "tag-key-2"})
	if duplicate.status != http.StatusConflict || decodeProblem(t, duplicate).Code != "NAME_DUPLICATED" {
		t.Fatalf("重名响应 = %d %q", duplicate.status, string(duplicate.body))
	}

	list := app.do(t, http.MethodGet, "/v1/tags?orderBy=name%20asc", "", bearer(session.AccessToken))
	var tags tagListBody
	if list.status != http.StatusOK || json.Unmarshal(list.body, &tags) != nil || len(tags.Items) != 1 {
		t.Fatalf("标签列表 = %d %q", list.status, string(list.body))
	}

	note := createNoteHTTP(t, app, session.AccessToken, root, "矩阵的秩", "正文", "tag-note-key-1")
	set := app.do(t, http.MethodPut, "/v1/notes/"+note.ID+"/tags",
		fmt.Sprintf(`{"tagIds":[%q]}`, created.ID), bearer(session.AccessToken))
	var setBody struct {
		Items []tagBody `json:"items"`
	}
	if set.status != http.StatusOK || json.Unmarshal(set.body, &setBody) != nil {
		t.Fatalf("设置笔记标签 = %d %q", set.status, string(set.body))
	}
	if len(setBody.Items) != 1 || setBody.Items[0].NoteCount != 1 {
		t.Fatalf("设置结果不符: %+v", setBody.Items)
	}

	/* 笔记详情应回填标签 */
	detail := app.do(t, http.MethodGet, "/v1/notes/"+note.ID, "", bearer(session.AccessToken))
	var detailTags noteTagsBody
	if detail.status != http.StatusOK || json.Unmarshal(detail.body, &detailTags) != nil {
		t.Fatalf("笔记详情 = %d %q", detail.status, string(detail.body))
	}
	if len(detailTags.Tags) != 1 || detailTags.Tags[0].ID != created.ID {
		t.Fatalf("笔记标签回填不符: %+v", detailTags.Tags)
	}

	/* 重命名后关联笔记展示新名称 */
	renamed := app.do(t, http.MethodPatch, "/v1/tags/"+created.ID, `{"name":"备考"}`, bearer(session.AccessToken))
	if renamed.status != http.StatusOK || decodeTag(t, renamed).Name != "备考" {
		t.Fatalf("重命名 = %d %q", renamed.status, string(renamed.body))
	}
	detail = app.do(t, http.MethodGet, "/v1/notes/"+note.ID, "", bearer(session.AccessToken))
	if json.Unmarshal(detail.body, &detailTags) != nil || detailTags.Tags[0].Name != "备考" {
		t.Fatalf("重命名未同步: %q", string(detail.body))
	}

	/* 超过 10 个标签直接拒绝，不查询标签本体 */
	eleven := make([]string, 0, 11)
	for index := 0; index < 11; index++ {
		eleven = append(eleven, fmt.Sprintf("tag-%d", index))
	}
	payload, err := json.Marshal(map[string]any{"tagIds": eleven})
	if err != nil {
		t.Fatalf("序列化请求: %v", err)
	}
	tooMany := app.do(t, http.MethodPut, "/v1/notes/"+note.ID+"/tags", string(payload), bearer(session.AccessToken))
	if tooMany.status != http.StatusConflict || decodeProblem(t, tooMany).Code != "TAG_LIMIT_EXCEEDED" {
		t.Fatalf("标签上限 = %d %q", tooMany.status, string(tooMany.body))
	}

	/* 删除标签只解除关联；重复删除返回 404 */
	if removed := app.do(t, http.MethodDelete, "/v1/tags/"+created.ID, "", bearer(session.AccessToken)); removed.status != http.StatusNoContent {
		t.Fatalf("删除标签状态码 = %d，原文 %q", removed.status, string(removed.body))
	}
	detail = app.do(t, http.MethodGet, "/v1/notes/"+note.ID, "", bearer(session.AccessToken))
	var afterDelete noteTagsBody
	if json.Unmarshal(detail.body, &afterDelete) != nil || len(afterDelete.Tags) != 0 {
		t.Fatalf("删除后仍有标签: %q", string(detail.body))
	}
	if again := app.do(t, http.MethodDelete, "/v1/tags/"+created.ID, "", bearer(session.AccessToken)); again.status != http.StatusNotFound {
		t.Fatalf("重复删除状态码 = %d，原文 %q", again.status, string(again.body))
	}
}

/* TestTagValidation 校验排序白名单与名称约束 */
func TestTagValidation(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "tag-invalid@example.com", "Password1")

	badOrder := app.do(t, http.MethodGet, "/v1/tags?orderBy=createdAt%20desc", "", bearer(session.AccessToken))
	if badOrder.status != http.StatusBadRequest || decodeProblem(t, badOrder).Code != "VALIDATION_FAILED" {
		t.Fatalf("非法 orderBy = %d %q", badOrder.status, string(badOrder.body))
	}
	badName := app.do(t, http.MethodPost, "/v1/tags", `{"name":"   "}`,
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "tag-bad-name"})
	if badName.status != http.StatusBadRequest || decodeProblem(t, badName).Code != "VALIDATION_FAILED" {
		t.Fatalf("非法名称 = %d %q", badName.status, string(badName.body))
	}
	badPage := app.do(t, http.MethodGet, "/v1/tags?pageSize=abc", "", bearer(session.AccessToken))
	if badPage.status != http.StatusBadRequest {
		t.Fatalf("非法 pageSize = %d %q", badPage.status, string(badPage.body))
	}
}
