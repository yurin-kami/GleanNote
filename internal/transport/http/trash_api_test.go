/*
 * 回收站端点的协议测试：目录批次聚合、纯笔记批次与分页参数。
 * 契约来源：docs/API.md#56-回收站、docs/openapi.yaml。
 */
package httptransport_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

/* trashEntryBody 与 openapi 的 TrashEntry 对齐 */
type trashEntryBody struct {
	BatchID       string `json:"batchId"`
	ItemType      string `json:"itemType"`
	ItemID        string `json:"itemId"`
	Name          string `json:"name"`
	OriginalPath  string `json:"originalPath"`
	FolderCount   int    `json:"folderCount"`
	NoteCount     int    `json:"noteCount"`
	DeletedAt     string `json:"deletedAt"`
	PurgeAt       string `json:"purgeAt"`
	RemainingDays int    `json:"remainingDays"`
}

/* trashListBody 是回收站集合响应 */
type trashListBody struct {
	Items         []trashEntryBody `json:"items"`
	NextPageToken string           `json:"nextPageToken"`
}

/* decodeTrash 解析回收站响应 */
func decodeTrash(t *testing.T, result httpResult) trashListBody {
	t.Helper()
	var body trashListBody
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("解析回收站响应失败: %v，原文 %q", err, string(result.body))
	}
	return body
}

/* TestTrashAggregatesFolderBatch 校验目录删除批次聚合整棵子树与其内笔记 */
func TestTrashAggregatesFolderBatch(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "trash-batch@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	math := createFolderHTTP(t, app, session.AccessToken, root, "数学", "trash-folder-key-1")
	createNoteHTTP(t, app, session.AccessToken, math.ID, "矩阵的秩", "正文", "trash-note-key-1")

	deleted := app.do(t, http.MethodDelete, "/v1/folders/"+math.ID, "", bearer(session.AccessToken))
	var deleteBody struct {
		DeleteBatchID string `json:"deleteBatchId"`
		FolderCount   int    `json:"folderCount"`
		NoteCount     int    `json:"noteCount"`
	}
	if deleted.status != http.StatusOK || json.Unmarshal(deleted.body, &deleteBody) != nil {
		t.Fatalf("删除目录 = %d %q", deleted.status, string(deleted.body))
	}

	list := app.do(t, http.MethodGet, "/v1/trash", "", bearer(session.AccessToken))
	if list.status != http.StatusOK {
		t.Fatalf("回收站列表 = %d %q", list.status, string(list.body))
	}
	body := decodeTrash(t, list)
	if len(body.Items) != 1 {
		t.Fatalf("回收站条目数 = %d，期望 1: %+v", len(body.Items), body.Items)
	}
	entry := body.Items[0]
	if entry.ItemType != "FOLDER" || entry.ItemID != math.ID || entry.Name != "数学" {
		t.Fatalf("批次类型或根节点不符: %+v", entry)
	}
	if entry.BatchID != deleteBody.DeleteBatchID || entry.FolderCount != 1 || entry.NoteCount != 1 {
		t.Fatalf("批次计数不符: %+v vs %+v", entry, deleteBody)
	}
	if entry.OriginalPath != "我的笔记 / 数学" || entry.RemainingDays != 30 {
		t.Fatalf("路径或剩余天数不符: %+v", entry)
	}
	if entry.DeletedAt != "2026-10-28T06:00:00Z" || entry.PurgeAt != "2026-11-27T06:00:00Z" {
		t.Fatalf("时间字段不符: %+v", entry)
	}

	/* 单独删除的笔记形成独立批次，itemType 为 NOTE */
	note := createNoteHTTP(t, app, session.AccessToken, root, "独立笔记", "正文", "trash-note-key-2")
	if removed := app.do(t, http.MethodDelete, "/v1/notes/"+note.ID, "", bearer(session.AccessToken)); removed.status != http.StatusOK {
		t.Fatalf("删除笔记 = %d %q", removed.status, string(removed.body))
	}
	body = decodeTrash(t, app.do(t, http.MethodGet, "/v1/trash", "", bearer(session.AccessToken)))
	if len(body.Items) != 2 {
		t.Fatalf("回收站条目数 = %d，期望 2: %+v", len(body.Items), body.Items)
	}
	var isolated *trashEntryBody
	for index := range body.Items {
		if body.Items[index].ItemID == note.ID {
			isolated = &body.Items[index]
		}
	}
	if isolated == nil || isolated.ItemType != "NOTE" || isolated.Name != "独立笔记" || isolated.FolderCount != 0 || isolated.NoteCount != 1 {
		t.Fatalf("独立笔记批次不符: %+v", isolated)
	}
}

/* TestTrashValidation 校验分页参数口径 */
func TestTrashValidation(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "trash-invalid@example.com", "Password1")

	badPageSize := app.do(t, http.MethodGet, "/v1/trash?pageSize=-1", "", bearer(session.AccessToken))
	if badPageSize.status != http.StatusBadRequest || decodeProblem(t, badPageSize).Code != "VALIDATION_FAILED" {
		t.Fatalf("非法 pageSize = %d %q", badPageSize.status, string(badPageSize.body))
	}
	tampered := app.do(t, http.MethodGet, "/v1/trash?pageToken=not-a-token", "", bearer(session.AccessToken))
	if tampered.status != http.StatusBadRequest || decodeProblem(t, tampered).Code != "INVALID_PAGE_TOKEN" {
		t.Fatalf("非法 pageToken = %d %q", tampered.status, string(tampered.body))
	}
}
