/*
 * Worker 装配测试：确认 GN_MODE=all 时进程内消费队列完成任务，GN_MODE=api 时不消费。
 * 规则来源：docs/ARCHITECTURE.md#55-异步与任务队列、docs/ARCHITECTURE.md#62-图片上传到转换完成。
 */
package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/bootstrap"
	"github.com/gleannote/gleannote/internal/config"
	"github.com/gleannote/gleannote/internal/infrastructure/mail"
)

/* codePattern 从开发态邮件正文中提取 6 位验证码 */
var codePattern = regexp.MustCompile(`\d{6}`)

/* mediaFlow 记录一次「上传→创建笔记」后可用于轮询的标识 */
type mediaFlow struct {
	token  string
	noteID string
	jobID  string
}

/* callAPI 通过装配好的路由执行一次请求，测试无需真实网络监听 */
func callAPI(t *testing.T, app *bootstrap.App, method, path, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	app.Router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

/* testPNG 生成一张可解码的纯色 PNG，用于走通上传完成校验 */
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			picture.Set(x, y, color.RGBA{R: 30, G: 90, B: 160, A: 255})
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

/*
 * runMediaFlow 走完整注册、上传与创建流程。
 * 验证码从开发态邮件端口的内存记录中读取，并确认标准输出已完成脱敏。
 */
func runMediaFlow(t *testing.T, app *bootstrap.App, mailOut *bytes.Buffer, email string) mediaFlow {
	t.Helper()

	status, body := callAPI(t, app, http.MethodPost, "/v1/auth/sendCode",
		fmt.Sprintf(`{"email":%q,"purpose":"REGISTER"}`, email), nil)
	if status != http.StatusNoContent {
		t.Fatalf("发送验证码 = %d %q", status, string(body))
	}
	fakeMailer, ok := app.Providers.Mailer.(*mail.Fake)
	if !ok {
		t.Fatalf("开发态邮件端口类型 = %T，期望 *mail.Fake", app.Providers.Mailer)
	}
	message, sent := fakeMailer.Last()
	if !sent {
		t.Fatal("邮件端口未记录本次投递")
	}
	code := codePattern.FindString(message.Body)
	if code == "" {
		t.Fatalf("邮件正文中没有验证码: %q", message.Body)
	}
	/* 日志输出不得含完整邮箱或明文验证码（G-10） */
	if strings.Contains(mailOut.String(), email) || strings.Contains(mailOut.String(), code) {
		t.Fatalf("开发态邮件输出未脱敏: %q", mailOut.String())
	}

	status, body = callAPI(t, app, http.MethodPost, "/v1/auth:register",
		fmt.Sprintf(`{"email":%q,"code":%q,"password":"Password1","agreements":{"userAgreement":true,"privacyPolicy":true}}`,
			email, code),
		map[string]string{"Idempotency-Key": "register-" + email})
	if status != http.StatusCreated {
		t.Fatalf("注册 = %d %q", status, string(body))
	}
	var session struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.AccessToken == "" {
		t.Fatalf("解析会话失败: %v，原文 %q", err, string(body))
	}
	auth := map[string]string{"Authorization": "Bearer " + session.AccessToken}

	status, body = callAPI(t, app, http.MethodGet, "/v1/folders", "", auth)
	if status != http.StatusOK {
		t.Fatalf("目录列表 = %d %q", status, string(body))
	}
	var folders struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &folders); err != nil || len(folders.Items) != 1 {
		t.Fatalf("解析目录列表失败: %v，原文 %q", err, string(body))
	}

	data := testPNG(t, 12, 8)
	status, body = callAPI(t, app, http.MethodPost, "/v1/uploads",
		fmt.Sprintf(`{"items":[{"fileName":"p1.png","contentType":"image/png","bytes":%d,"sha256":%q}]}`,
			len(data), digest(data)), auth)
	if status != http.StatusCreated {
		t.Fatalf("申请上传 = %d %q", status, string(body))
	}
	var tickets struct {
		Items []struct {
			UploadID  string `json:"uploadId"`
			ObjectKey string `json:"objectKey"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &tickets); err != nil || len(tickets.Items) != 1 {
		t.Fatalf("解析上传会话失败: %v，原文 %q", err, string(body))
	}
	ticket := tickets.Items[0]

	/* 模拟客户端直传：由服务端写入同一 Fake 对象存储 */
	if err := app.Providers.ObjectStore.Put(context.Background(), ticket.ObjectKey, "image/png", data); err != nil {
		t.Fatalf("写入对象: %v", err)
	}
	status, body = callAPI(t, app, http.MethodPost, "/v1/uploads/"+ticket.UploadID+":complete",
		fmt.Sprintf(`{"sha256":%q}`, digest(data)), auth)
	if status != http.StatusOK {
		t.Fatalf("完成上传 = %d %q", status, string(body))
	}

	status, body = callAPI(t, app, http.MethodPost, "/v1/notes:createFromPages",
		fmt.Sprintf(`{"folderId":%q,"uploadIds":[%q]}`, folders.Items[0].ID, ticket.UploadID),
		map[string]string{"Authorization": "Bearer " + session.AccessToken, "Idempotency-Key": "create-" + email})
	if status != http.StatusCreated {
		t.Fatalf("创建笔记 = %d %q", status, string(body))
	}
	var created struct {
		Note struct {
			ID string `json:"id"`
		} `json:"note"`
		ConversionJob struct {
			ID string `json:"id"`
		} `json:"conversionJob"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Note.ID == "" || created.ConversionJob.ID == "" {
		t.Fatalf("解析创建响应失败: %v，原文 %q", err, string(body))
	}
	return mediaFlow{token: session.AccessToken, noteID: created.Note.ID, jobID: created.ConversionJob.ID}
}

/* jobState 查询当前转换任务状态 */
func jobState(t *testing.T, app *bootstrap.App, flow mediaFlow) (string, string) {
	t.Helper()
	status, body := callAPI(t, app, http.MethodGet,
		"/v1/notes/"+flow.noteID+"/conversionJob", "",
		map[string]string{"Authorization": "Bearer " + flow.token})
	if status != http.StatusOK {
		t.Fatalf("任务查询 = %d %q", status, string(body))
	}
	var job struct {
		State       string `json:"state"`
		FailureCode string `json:"failureCode"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatalf("解析任务失败: %v，原文 %q", err, string(body))
	}
	return job.State, job.FailureCode
}

/* 启动 all 模式的装配应消费队列，验收逐笔画入与任务终态 */
func TestWorkerConvertsQueuedJob(t *testing.T) {
	mailOut := &bytes.Buffer{}
	app, err := bootstrap.New(devConfig("all"), mailOut, quietLogger())
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			t.Fatalf("关闭失败: %v", closeErr)
		}
	}()

	flow := runMediaFlow(t, app, mailOut, "worker-all@example.com")

	deadline := time.Now().Add(5 * time.Second)
	for {
		state, failure := jobState(t, app, flow)
		if state == "SUCCEEDED" {
			break
		}
		if state == "FAILED" || state == "CANCELED" {
			t.Fatalf("任务终态 = %s（%s），期望 SUCCEEDED", state, failure)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待任务完成超时，当前状态 %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	status, body := callAPI(t, app, http.MethodGet, "/v1/notes/"+flow.noteID, "",
		map[string]string{"Authorization": "Bearer " + flow.token})
	if status != http.StatusOK {
		t.Fatalf("笔记详情 = %d %q", status, string(body))
	}
	var note struct {
		State   string `json:"state"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("解析笔记失败: %v，原文 %q", err, string(body))
	}
	if note.State != "NORMAL" || note.Content == "" {
		t.Fatalf("转换结果不符: %+v", note)
	}
	/* Fake 供应商输出只有二级标题，标题应回落到「未命名笔记」 */
	if !strings.HasPrefix(note.Title, "未命名笔记") {
		t.Fatalf("标题 = %q，期望回落为未命名笔记", note.Title)
	}
}

/* api 模式不启动 Worker：任务应停留在 QUEUED，等待独立 worker 进程消费 */
func TestAPIModeDoesNotConsumeQueue(t *testing.T) {
	mailOut := &bytes.Buffer{}
	app, err := bootstrap.New(devConfig("api"), mailOut, quietLogger())
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			t.Fatalf("关闭失败: %v", closeErr)
		}
	}()

	flow := runMediaFlow(t, app, mailOut, "worker-api@example.com")
	/* api 模式没有任何消费协程：创建响应返回时任务已持久化为 QUEUED，无需用等待时间反证 */
	if state, failure := jobState(t, app, flow); state != "QUEUED" {
		t.Fatalf("任务状态 = %s（%s），期望 QUEUED", state, failure)
	}
}

/* devConfig 生成最小开发态配置：提供者留空即 fake，仅需 HMAC 密钥与公开地址 */
func devConfig(mode string) config.Config {
	return config.Config{
		Env:           "dev",
		Mode:          mode,
		PublicBaseURL: "https://gleannote.example",
		Auth:          config.AuthConfig{HMACSecret: strings.Repeat("k", 32)},
	}
}
