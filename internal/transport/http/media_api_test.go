/*
 * 上传、页面与转换端点的协议测试：预签名、完成校验、页面地址与任务生命周期。
 * 契约来源：docs/API.md#54-图片上传与页面、docs/API.md#55-转换任务。
 */
package httptransport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"
)

/* uploadSessionBody 对应 openapi 的 UploadSession */
type uploadSessionBody struct {
	UploadID  string            `json:"uploadId"`
	ObjectKey string            `json:"objectKey"`
	PutURL    string            `json:"putUrl"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt string            `json:"expiresAt"`
}

/* uploadSessionsBody 是批量申请响应 */
type uploadSessionsBody struct {
	Items []uploadSessionBody `json:"items"`
}

/* uploadedPageBody 对应 openapi 的 UploadedPage */
type uploadedPageBody struct {
	UploadID string `json:"uploadId"`
	Status   string `json:"status"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Bytes    int64  `json:"bytes"`
	ThumbURL string `json:"thumbUrl"`
}

/* pageBody 对应 openapi 的 Page */
type pageBody struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Bytes    int64  `json:"bytes"`
	ThumbURL string `json:"thumbUrl"`
}

/* pageListBody 是页面集合响应 */
type pageListBody struct {
	Items         []pageBody `json:"items"`
	NextPageToken string     `json:"nextPageToken"`
}

/* pageOriginalBody 对应 openapi 的 PageOriginal */
type pageOriginalBody struct {
	ID          string `json:"id"`
	Index       int    `json:"index"`
	OriginalURL string `json:"originalUrl"`
	ExpiresAt   string `json:"expiresAt"`
}

/* conversionJobBody 对应 openapi 的 ConversionJob */
type conversionJobBody struct {
	ID             string `json:"id"`
	NoteID         string `json:"noteId"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	Phase          string `json:"phase"`
	TotalPages     int    `json:"totalPages"`
	ProcessedPages int    `json:"processedPages"`
	FailureCode    string `json:"failureCode"`
	QuotaCharged   int    `json:"quotaCharged"`
	CreatedAt      string `json:"createdAt"`
}

/* createFromPagesBody 是创建笔记响应 */
type createFromPagesBody struct {
	Note          noteBody          `json:"note"`
	ConversionJob conversionJobBody `json:"conversionJob"`
	Quota         struct {
		Remaining int `json:"remaining"`
		Charged   int `json:"charged"`
	} `json:"quota"`
}

/* testPNG 生成一张可解码的纯色 PNG，用于走通上传完成校验 */
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			picture.Set(x, y, color.RGBA{R: 200, G: 120, B: 60, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		t.Fatalf("编码测试图片: %v", err)
	}
	return buffer.Bytes()
}

/* digest 返回小写十六进制 SHA256 */
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

/* requestUpload 申请一次上传会话并要求 201 */
func requestUpload(t *testing.T, app *testApp, token string, payload string) uploadSessionBody {
	t.Helper()
	result := app.do(t, http.MethodPost, "/v1/uploads", payload, bearer(token))
	if result.status != http.StatusCreated {
		t.Fatalf("申请上传状态码 = %d，原文 %q", result.status, string(result.body))
	}
	var body uploadSessionsBody
	if err := jsonUnmarshal(result.body, &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("解析上传会话失败: %v，原文 %q", err, string(result.body))
	}
	return body.Items[0]
}

/* TestUploadCompleteFlow 校验会话有效期、完成校验与缩略图地址 */
func TestUploadCompleteFlow(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "upload@example.com", "Password1")
	data := testPNG(t, 40, 30)

	ticket := requestUpload(t, app, session.AccessToken,
		fmt.Sprintf(`{"items":[{"fileName":"p1.png","contentType":"image/png","bytes":%d,"sha256":%q}]}`,
			len(data), digest(data)))
	if ticket.UploadID == "" || ticket.PutURL == "" || ticket.ExpiresAt != "2026-10-28T06:30:00Z" {
		t.Fatalf("上传会话 = %+v", ticket)
	}
	if ticket.Headers["Content-Type"] != "image/png" {
		t.Fatalf("强制头 = %+v", ticket.Headers)
	}

	/* 模拟客户端直传：把对象写入同一 Fake 存储 */
	if err := app.objects.Put(context.Background(), ticket.ObjectKey, "image/png", data); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	completed := app.do(t, http.MethodPost,
		"/v1/uploads/"+ticket.UploadID+":complete",
		fmt.Sprintf(`{"sha256":%q}`, digest(data)), bearer(session.AccessToken))
	if completed.status != http.StatusOK {
		t.Fatalf("完成校验状态码 = %d，原文 %q", completed.status, string(completed.body))
	}
	var page uploadedPageBody
	if err := jsonUnmarshal(completed.body, &page); err != nil {
		t.Fatalf("解析完成响应: %v", err)
	}
	if page.Status != "READY" || page.Width != 40 || page.Height != 30 || page.ThumbURL == "" {
		t.Fatalf("完成响应 = %+v", page)
	}

	/* 摘要不符返回 409，且消息码为 UPLOAD_HASH_MISMATCH */
	mismatch := app.do(t, http.MethodPost,
		"/v1/uploads/"+ticket.UploadID+":complete",
		fmt.Sprintf(`{"sha256":%q}`, digest([]byte("other"))), bearer(session.AccessToken))
	if mismatch.status != http.StatusConflict || decodeProblem(t, mismatch).Code != "UPLOAD_HASH_MISMATCH" {
		t.Fatalf("摘要不符 = %d %q", mismatch.status, string(mismatch.body))
	}
}

/* TestCreateFromPagesFlow 覆盖创建草稿、页面读取、任务查询、取消与重试 */
func TestCreateFromPagesFlow(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "pages@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	data := testPNG(t, 20, 10)

	ticket := requestUpload(t, app, session.AccessToken,
		fmt.Sprintf(`{"items":[{"fileName":"p1.png","contentType":"image/png","bytes":%d,"sha256":%q}]}`,
			len(data), digest(data)))
	if err := app.objects.Put(context.Background(), ticket.ObjectKey, "image/png", data); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	completed := app.do(t, http.MethodPost,
		"/v1/uploads/"+ticket.UploadID+":complete",
		fmt.Sprintf(`{"sha256":%q}`, digest(data)), bearer(session.AccessToken))
	if completed.status != http.StatusOK {
		t.Fatalf("完成校验 = %d %q", completed.status, string(completed.body))
	}

	created := app.do(t, http.MethodPost, "/v1/notes:createFromPages",
		fmt.Sprintf(`{"folderId":%q,"uploadIds":[%q]}`, root, ticket.UploadID),
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "pages-key-1",
		})
	if created.status != http.StatusCreated {
		t.Fatalf("创建笔记状态码 = %d，原文 %q", created.status, string(created.body))
	}
	var body createFromPagesBody
	if err := jsonUnmarshal(created.body, &body); err != nil {
		t.Fatalf("解析创建响应: %v", err)
	}
	if body.Note.State != "DRAFT" || body.Note.PageCount != 1 || body.Note.ThumbnailURL == "" {
		t.Fatalf("笔记 = %+v", body.Note)
	}
	if body.ConversionJob.State != "QUEUED" || body.ConversionJob.TotalPages != 1 {
		t.Fatalf("任务 = %+v", body.ConversionJob)
	}
	if body.Quota.Charged != 1 || body.Quota.Remaining != 49 {
		t.Fatalf("额度 = %+v", body.Quota)
	}

	pages := app.do(t, http.MethodGet, "/v1/notes/"+body.Note.ID+"/pages", "", bearer(session.AccessToken))
	if pages.status != http.StatusOK {
		t.Fatalf("页面列表状态码 = %d，原文 %q", pages.status, string(pages.body))
	}
	var pageList pageListBody
	if err := jsonUnmarshal(pages.body, &pageList); err != nil || len(pageList.Items) != 1 {
		t.Fatalf("页面列表 = %+v，err=%v", pageList, err)
	}
	if pageList.Items[0].Index != 1 || pageList.Items[0].ThumbURL == "" {
		t.Fatalf("页面项 = %+v", pageList.Items[0])
	}

	original := app.do(t, http.MethodGet,
		"/v1/notes/"+body.Note.ID+"/pages/"+pageList.Items[0].ID, "", bearer(session.AccessToken))
	if original.status != http.StatusOK {
		t.Fatalf("原图地址状态码 = %d，原文 %q", original.status, string(original.body))
	}
	var originalBody pageOriginalBody
	if err := jsonUnmarshal(original.body, &originalBody); err != nil {
		t.Fatalf("解析原图响应: %v", err)
	}
	if originalBody.OriginalURL == "" || originalBody.ExpiresAt != "2026-10-28T06:10:00Z" {
		t.Fatalf("原图地址 = %+v", originalBody)
	}

	job := app.do(t, http.MethodGet, "/v1/notes/"+body.Note.ID+"/conversionJob", "", bearer(session.AccessToken))
	if job.status != http.StatusOK {
		t.Fatalf("任务查询状态码 = %d，原文 %q", job.status, string(job.body))
	}

	canceled := app.do(t, http.MethodPost,
		"/v1/notes/"+body.Note.ID+"/conversionJob:cancel", "", bearer(session.AccessToken))
	if canceled.status != http.StatusOK {
		t.Fatalf("取消失败 = %d %q", canceled.status, string(canceled.body))
	}
	var canceledJob conversionJobBody
	if err := jsonUnmarshal(canceled.body, &canceledJob); err != nil || canceledJob.State != "CANCELED" {
		t.Fatalf("取消结果 = %+v，err=%v", canceledJob, err)
	}

	retried := app.do(t, http.MethodPost,
		"/v1/notes/"+body.Note.ID+"/conversionJob:retry", "",
		map[string]string{
			"Authorization":   "Bearer " + session.AccessToken,
			"Idempotency-Key": "pages-retry-1",
		})
	if retried.status != http.StatusAccepted {
		t.Fatalf("重试状态码 = %d，原文 %q", retried.status, string(retried.body))
	}
	var retriedJob conversionJobBody
	if err := jsonUnmarshal(retried.body, &retriedJob); err != nil || retriedJob.State != "QUEUED" {
		t.Fatalf("重试结果 = %+v，err=%v", retriedJob, err)
	}
	if retriedJob.ID == canceledJob.ID {
		t.Fatal("重试应创建新任务")
	}
}

/* TestMediaErrors 校验鉴权、分页参数与未提交任务的状态码口径 */
func TestMediaErrors(t *testing.T) {
	app := newTestApp(t)
	session := app.registerSession(t, "media-err@example.com", "Password1")
	root := rootFolderID(t, app, session.AccessToken)
	note := createNoteHTTP(t, app, session.AccessToken, root, "空白", "", "media-note-1")

	if result := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/conversionJob", "", nil); result.status != http.StatusUnauthorized {
		t.Fatalf("缺少令牌状态码 = %d", result.status)
	}

	negative := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/pages?pageSize=-1", "", bearer(session.AccessToken))
	if negative.status != http.StatusBadRequest {
		t.Fatalf("负数 pageSize 状态码 = %d，原文 %q", negative.status, string(negative.body))
	}

	missing := app.do(t, http.MethodGet, "/v1/notes/"+note.ID+"/conversionJob", "", bearer(session.AccessToken))
	if missing.status != http.StatusNotFound || decodeProblem(t, missing).Code != "CONVERSION_NOT_FOUND" {
		t.Fatalf("无任务 = %d %q", missing.status, string(missing.body))
	}

	/* 不合法的上传声明应返回 400，且不创建会话 */
	bad := app.do(t, http.MethodPost, "/v1/uploads",
		`{"items":[{"fileName":"a.heic","contentType":"image/heic","bytes":1024,"sha256":"`+digest([]byte("x"))+`"}]}`,
		bearer(session.AccessToken))
	if bad.status != http.StatusBadRequest {
		t.Fatalf("不支持的格式状态码 = %d，原文 %q", bad.status, string(bad.body))
	}
}
