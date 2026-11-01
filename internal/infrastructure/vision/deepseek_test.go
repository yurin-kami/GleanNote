/*
 * DeepSeek 适配器的单元测试：锁定请求构造、分块、返回清洗与错误归一。
 * 全程使用 httptest，不访问真实供应商，也不产生调用费用。
 */
package vision

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* testPageBytes 是占位图片字节；适配器不解码图片，仅按响应头判定类型 */
var testPageBytes = []byte("fake-page-bytes")

/*
 * TestDeepSeekConvertBuildsInlineRequest 校验：
 * 页面以 base64 data URL 内联、按页附加序号、使用 Bearer 鉴权、返回正文与未识别页码。
 */
func TestDeepSeekConvertBuildsInlineRequest(t *testing.T) {
	var calls int32
	bodies := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page/1.png", "/page/2.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPageBytes)
		case "/chat/completions":
			atomic.AddInt32(&calls, 1)
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
				t.Errorf("Authorization = %q，期望 Bearer test-key", auth)
			}
			body, _ := io.ReadAll(r.Body)
			bodies <- string(body)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"# 文档标题\n\n## 第 1 页\n\n正文内容\n\n## 第 2 页\n\n（第 2 页未识别到文字）"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL, Model: "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	if provider.Name() != "deepseek" {
		t.Fatalf("Name = %q，期望 deepseek", provider.Name())
	}

	result, err := provider.Convert(context.Background(), []port.VisionPage{
		{Index: 1, URL: server.URL + "/page/1.png"},
		{Index: 2, URL: server.URL + "/page/2.png"},
	})
	if err != nil {
		t.Fatalf("Convert 返回错误: %v", err)
	}
	if result.Provider != "deepseek" {
		t.Fatalf("Provider = %q，期望 deepseek", result.Provider)
	}
	if !strings.Contains(result.Markdown, "# 文档标题") {
		t.Fatalf("Markdown 缺少文档标题: %q", result.Markdown)
	}
	if len(result.UnreadablePages) != 1 || result.UnreadablePages[0] != 2 {
		t.Fatalf("UnreadablePages = %v，期望 [2]", result.UnreadablePages)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("调用次数 = %d，期望 1（两页合并为一次请求）", got)
	}

	raw := <-bodies
	for _, want := range []string{
		`"model":"deepseek-v4-pro"`,
		`"stream":false`,
		"排版引擎",
		"第 1 页：",
		"第 2 页：",
		"data:image/png;base64,",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("请求体缺少 %q: %s", want, raw)
		}
	}
}

/* TestDeepSeekConvertChunksPages 校验超过单请求页数上限时按块调用并拼接结果 */
func TestDeepSeekConvertChunksPages(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPageBytes)
			return
		}
		index := atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"## 分块 `+string(rune('0'+index))+`"}}]}`)
	}))
	defer server.Close()

	provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	pages := make([]port.VisionPage, 0, 5)
	for index := 1; index <= 5; index++ {
		pages = append(pages, port.VisionPage{Index: index, URL: server.URL + "/page.png"})
	}
	result, err := provider.Convert(context.Background(), pages)
	if err != nil {
		t.Fatalf("Convert 返回错误: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("调用次数 = %d，期望 2（5 页拆为 4+1）", got)
	}
	if !strings.Contains(result.Markdown, "## 分块 1") || !strings.Contains(result.Markdown, "## 分块 2") {
		t.Fatalf("分块结果未按序拼接: %q", result.Markdown)
	}
}

/* TestDeepSeekMapsProviderErrors 校验 HTTP 状态与错误文本被归一为端口哨兵错误 */
func TestDeepSeekMapsProviderErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"鉴权失败", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`, port.ErrProviderNotConfigured},
		{"限流", http.StatusTooManyRequests, `{"error":{"message":"rate limit"}}`, port.ErrProviderUnavailable},
		{"服务端错误", http.StatusInternalServerError, `{}`, port.ErrProviderUnavailable},
		{"图片被拒绝", http.StatusBadRequest, `{"error":{"message":"invalid image format"}}`, port.ErrImageUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(testPageBytes)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("构造适配器失败: %v", err)
			}
			_, err = provider.Convert(context.Background(), []port.VisionPage{{Index: 1, URL: server.URL + "/page.png"}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("返回 %v，期望 %v", err, tc.want)
			}
		})
	}
}

/* TestDeepSeekRejectsUnsupportedPage 校验非图片页面在调用模型前即被拒绝 */
func TestDeepSeekRejectsUnsupportedPage(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>not an image</html>")
			return
		}
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"不应到达"}}]}`)
	}))
	defer server.Close()

	provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	_, err = provider.Convert(context.Background(), []port.VisionPage{{Index: 1, URL: server.URL + "/page.html"}})
	if !errors.Is(err, port.ErrContentUnsupported) {
		t.Fatalf("返回 %v，期望 ErrContentUnsupported", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("调用次数 = %d，期望 0", got)
	}
}

/* TestDeepSeekMapsPageDownloadFailure 校验预签名地址失效时归一为供应商不可用 */
func TestDeepSeekMapsPageDownloadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	_, err = provider.Convert(context.Background(), []port.VisionPage{{Index: 1, URL: server.URL + "/page.png"}})
	if !errors.Is(err, port.ErrProviderUnavailable) {
		t.Fatalf("返回 %v，期望 ErrProviderUnavailable", err)
	}
}

/* TestDeepSeekStripsWholeOutputFence 校验整篇被三反引号包裹时去掉外包装 */
func TestDeepSeekStripsWholeOutputFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(testPageBytes)
			return
		}
		_, _ = io.WriteString(w, "{\"choices\":[{\"message\":{\"content\":\"```markdown\\n# 标题\\n\\n正文\\n```\"}}]}")
	}))
	defer server.Close()

	provider, err := NewDeepSeek(Options{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	result, err := provider.Convert(context.Background(), []port.VisionPage{{Index: 1, URL: server.URL + "/page.jpg"}})
	if err != nil {
		t.Fatalf("Convert 返回错误: %v", err)
	}
	if result.Markdown != "# 标题\n\n正文" {
		t.Fatalf("Markdown = %q，期望去掉外层代码块", result.Markdown)
	}
}

/* TestNewDeepSeekRequiresAPIKey 校验缺少凭据必须在装配期失败 */
func TestNewDeepSeekRequiresAPIKey(t *testing.T) {
	if _, err := New(Options{Name: "deepseek"}); !errors.Is(err, port.ErrProviderNotConfigured) {
		t.Fatalf("返回 %v，期望 ErrProviderNotConfigured", err)
	}
	provider, err := NewDeepSeek(Options{APIKey: "key", BaseURL: "https://api.deepseek.com"})
	if err != nil {
		t.Fatalf("合法配置构造失败: %v", err)
	}
	if err := provider.Health(context.Background()); err != nil {
		t.Fatalf("Health 返回错误: %v", err)
	}
	parsed, err := url.Parse(provider.endpoint)
	if err != nil || parsed.Path != "/chat/completions" {
		t.Fatalf("endpoint = %q，期望以 /chat/completions 结尾", provider.endpoint)
	}
}
