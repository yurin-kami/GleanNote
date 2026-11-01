/*
 * 导出与批量导入端点的协议测试：202/200 语义、错误码与逐项结果。
 * 契约来源：docs/API.md#511-导出与导入、PRD FR-14/FR-17。
 */
package httptransport_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

/* exportBody 对应 openapi 的 Export */
type exportBody struct {
	ExportID    string `json:"exportId"`
	State       string `json:"state"`
	DownloadURL string `json:"downloadUrl"`
	ExpiresAt   string `json:"expiresAt"`
	FailureCode string `json:"failureCode"`
}

/* importItemBody 对应 openapi 的 Import.items 单项 */
type importItemBody struct {
	UploadID    string `json:"uploadId"`
	NoteID      string `json:"noteId"`
	State       string `json:"state"`
	FailureCode string `json:"failureCode"`
}

/* importBody 对应 openapi 的 Import */
type importBody struct {
	ImportID  string           `json:"importId"`
	State     string           `json:"state"`
	Total     int              `json:"total"`
	Succeeded int              `json:"succeeded"`
	Failed    int              `json:"failed"`
	Items     []importItemBody `json:"items"`
}

/* completeUpload 走完一次上传并返回会话，供导入用例复用 */
func completeUpload(t *testing.T, app *testApp, token string) uploadSessionBody {
	t.Helper()
	data := testPNG(t, 20, 10)
	ticket := requestUpload(t, app, token,
		fmt.Sprintf(`{"items":[{"fileName":"p.png","contentType":"image/png","bytes":%d,"sha256":%q}]}`,
			len(data), digest(data)))
	if err := app.objects.Put(context.Background(), ticket.ObjectKey, "image/png", data); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	completed := app.do(t, http.MethodPost, "/v1/uploads/"+ticket.UploadID+":complete",
		fmt.Sprintf(`{"sha256":%q}`, digest(data)), bearer(token))
	if completed.status != http.StatusOK {
		t.Fatalf("完成校验 = %d %q", completed.status, string(completed.body))
	}
	return ticket
}

/* TestExportAPI 校验导出发起、状态查询与参数错误 */
func TestExportAPI(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "export@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	note := createNoteHTTP(t, app, session.AccessToken, root, "导出笔记", "正文内容", "export-note-1")

	created := app.do(t, http.MethodPost, "/v1/exports",
		fmt.Sprintf(`{"noteId":%q,"format":"MARKDOWN_ZIP"}`, note.ID),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "export-key-1",
		})
	if created.status != http.StatusAccepted {
		t.Fatalf("发起导出状态码 = %d，原文 %q", created.status, string(created.body))
	}
	var export exportBody
	if err := jsonUnmarshal(created.body, &export); err != nil {
		t.Fatalf("解析导出响应: %v", err)
	}
	if export.ExportID == "" || export.State != "SUCCEEDED" || export.DownloadURL == "" || export.ExpiresAt == "" {
		t.Fatalf("导出响应 = %+v", export)
	}

	fetched := app.do(t, http.MethodGet, "/v1/exports/"+export.ExportID, "",
		bearer(session.AccessToken))
	if fetched.status != http.StatusOK {
		t.Fatalf("查询导出状态码 = %d，原文 %q", fetched.status, string(fetched.body))
	}
	var fetchedBody exportBody
	if err := jsonUnmarshal(fetched.body, &fetchedBody); err != nil {
		t.Fatalf("解析查询响应: %v", err)
	}
	if fetchedBody.DownloadURL == "" || fetchedBody.State != "SUCCEEDED" {
		t.Fatalf("查询响应 = %+v", fetchedBody)
	}

	pdf := app.do(t, http.MethodPost, "/v1/exports",
		fmt.Sprintf(`{"noteId":%q,"format":"PDF"}`, note.ID),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "export-key-2",
		})
	if pdf.status != http.StatusAccepted {
		t.Fatalf("PDF 导出状态码 = %d，原文 %q", pdf.status, string(pdf.body))
	}

	/* 参数与可见性错误 */
	both := app.do(t, http.MethodPost, "/v1/exports",
		fmt.Sprintf(`{"noteId":%q,"folderId":%q,"format":"PDF"}`, note.ID, root),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "export-key-3",
		})
	if both.status != http.StatusBadRequest {
		t.Fatalf("双目标状态码 = %d，原文 %q", both.status, string(both.body))
	}
	badFormat := app.do(t, http.MethodPost, "/v1/exports",
		fmt.Sprintf(`{"noteId":%q,"format":"DOCX"}`, note.ID),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "export-key-4",
		})
	if badFormat.status != http.StatusBadRequest {
		t.Fatalf("非法格式状态码 = %d，原文 %q", badFormat.status, string(badFormat.body))
	}
	missing := app.do(t, http.MethodGet, "/v1/exports/export-missing", "", bearer(session.AccessToken))
	if missing.status != http.StatusNotFound || decodeProblem(t, missing).Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("不存在的导出 = %d %q", missing.status, string(missing.body))
	}
}

/* TestImportAPI 校验逐项状态、部分失败与查询 */
func TestImportAPI(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "import@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	first := completeUpload(t, app, session.AccessToken)
	second := completeUpload(t, app, session.AccessToken)

	created := app.do(t, http.MethodPost, "/v1/imports",
		fmt.Sprintf(`{"folderId":%q,"mode":"PER_IMAGE","uploadIds":[%q,%q,"upload-missing"]}`,
			root, first.UploadID, second.UploadID),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "import-key-1",
		})
	if created.status != http.StatusAccepted {
		t.Fatalf("发起导入状态码 = %d，原文 %q", created.status, string(created.body))
	}
	var imported importBody
	if err := jsonUnmarshal(created.body, &imported); err != nil {
		t.Fatalf("解析导入响应: %v", err)
	}
	if imported.ImportID == "" || imported.State != "PARTIAL_FAILED" ||
		imported.Total != 3 || imported.Succeeded != 2 || imported.Failed != 1 {
		t.Fatalf("导入响应 = %+v", imported)
	}
	if len(imported.Items) != 3 || imported.Items[0].NoteID == "" ||
		imported.Items[2].State != "FAILED" || imported.Items[2].FailureCode != "UPLOAD_NOT_FOUND" {
		t.Fatalf("导入项 = %+v", imported.Items)
	}

	fetched := app.do(t, http.MethodGet, "/v1/imports/"+imported.ImportID, "",
		bearer(session.AccessToken))
	if fetched.status != http.StatusOK {
		t.Fatalf("查询导入状态码 = %d，原文 %q", fetched.status, string(fetched.body))
	}
	var fetchedBody importBody
	if err := jsonUnmarshal(fetched.body, &fetchedBody); err != nil {
		t.Fatalf("解析查询响应: %v", err)
	}
	if fetchedBody.ImportID != imported.ImportID || fetchedBody.Succeeded != 2 {
		t.Fatalf("查询响应 = %+v", fetchedBody)
	}

	/* 未鉴权与不存在的任务 */
	if result := app.do(t, http.MethodPost, "/v1/imports",
		fmt.Sprintf(`{"folderId":%q,"mode":"MERGE","uploadIds":[%q]}`, root, first.UploadID), nil); result.status != http.StatusUnauthorized {
		t.Fatalf("未鉴权状态码 = %d", result.status)
	}
	if result := app.do(t, http.MethodGet, "/v1/imports/import-missing", "", bearer(session.AccessToken)); result.status != http.StatusNotFound {
		t.Fatalf("不存在的导入 = %d %q", result.status, string(result.body))
	}
}
