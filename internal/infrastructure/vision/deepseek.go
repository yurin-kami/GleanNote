/*
 * DeepSeek 视觉适配器：通过 OpenAI Chat Completions 兼容接口完成「图片 → Markdown」。
 *
 * 职责：下载预签名页面图片、按页分块、内联 base64 提交、清洗返回值并归一错误。
 * 不负责：额度结算、任务状态机与对象存储读写（均由用例层完成）。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
)

const (
	/* deepseekBaseURL 是官方 OpenAI 兼容端点；可用 GN_VISION_BASE_URL 指向其他兼容服务 */
	deepseekBaseURL = "https://api.deepseek.com"
	/* deepseekModel 是默认多模态模型；可用 GN_VISION_MODEL 覆盖 */
	deepseekModel = "deepseek-flash"
	/* defaultVisionTimeout 是单次 HTTP 调用的超时；整篇转换仍受用例层 5 分钟上限约束 */
	defaultVisionTimeout = 2 * time.Minute
	/* pagesPerRequest 控制单次请求的图片张数，避免 20 页原图超出模型请求体积 */
	pagesPerRequest = 4
	/* maxPageBytes 是单页下载上限；上传侧已限制 10 MB，超出视为异常内容 */
	maxPageBytes = 15 << 20
	/* maxChatResponseBytes 防御异常响应体，正常 Markdown 远小于该值 */
	maxChatResponseBytes = 4 << 20
)

/* unreadablePattern 匹配 PRD FR-05 约定的未识别占位，并提取页码 */
var unreadablePattern = regexp.MustCompile(`（第\s*(\d+)\s*页未识别到文字）`)

/*
 * layoutPrompt 是排版转写指令。
 * 要求按阅读顺序与版式还原，逐页分段，并在首页保留文档标题供用例派生笔记名。
 */
const layoutPrompt = `你是文档 OCR 与排版引擎。请把随附图片按顺序逐页转写为 GitHub Flavored Markdown，忠实还原版式：
1. 阅读顺序：先标题区，再正文（多栏时按栏从左到右、栏内从上到下），最后页脚与脚注。
2. 结构：保留标题层级（#、##、###）、段落、有序与无序列表、引用块、表格（含表头）、代码块（标注语言）与分隔线。
3. 公式：行内用 $...$，独立公式用 $$...$$，按 LaTeX 书写。
4. 手写与印刷混排按实际内容转写，不要臆造、不要补全缺失内容。
5. 每页以「## 第 N 页」开头，N 为该页序号。
6. 某页没有可识别文字（空白页、纯图、严重模糊）时，只输出「（第 N 页未识别到文字）」。
7. 若在首页识别到文档标题，请在最前面额外输出一行「# 标题」，不要重复输出。
8. 只输出 Markdown 正文，不要用三反引号包裹整篇，也不要任何解释或前后缀。`

/*
 * DeepSeek 是 port.VisionProvider 的 OpenAI 兼容实现。
 * 同一实现可用于任何兼容 /chat/completions 且支持图片输入的服务，只需改 base URL 与模型名。
 */
type DeepSeek struct {
	apiKey   string
	endpoint string
	model    string
	client   *http.Client
}

/* chatRequest 是 Chat Completions 请求体；Content 允许字符串或分段数组 */
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

/* contentPart 是用户消息的分段；文本与图片按页交错，便于模型对齐页码 */
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

/*
 * NewDeepSeek 创建适配器。
 * 缺少 API Key 视为配置错误，必须在装配期失败，禁止带着空凭据运行。
 */
func NewDeepSeek(opts Options) (*DeepSeek, error) {
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: GN_VISION_API_KEY 未设置", port.ErrProviderNotConfigured)
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = deepseekBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: GN_VISION_BASE_URL 非法: %q", port.ErrProviderNotConfigured, baseURL)
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = deepseekModel
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultVisionTimeout
	}
	return &DeepSeek{
		apiKey:   apiKey,
		endpoint: baseURL + "/chat/completions",
		model:    model,
		client:   &http.Client{Timeout: timeout},
	}, nil
}

/* Name 返回提供者名称，用于启动日志与就绪探针展示 */
func (d *DeepSeek) Name() string { return "deepseek" }

/*
 * Convert 按页分块调用模型并拼接结果。
 * 分块只影响请求体积，不影响返回的页码与整篇顺序。
 */
func (d *DeepSeek) Convert(ctx context.Context, pages []port.VisionPage) (*port.VisionResult, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: 没有待识别页面", port.ErrContentUnsupported)
	}
	var markdown strings.Builder
	unreadable := make([]int, 0, len(pages))
	for start := 0; start < len(pages); start += pagesPerRequest {
		end := min(start+pagesPerRequest, len(pages))
		chunk, err := d.convertChunk(ctx, pages[start:end])
		if err != nil {
			return nil, err
		}
		if markdown.Len() > 0 {
			markdown.WriteString("\n\n")
		}
		markdown.WriteString(chunk)
		unreadable = append(unreadable, findUnreadable(chunk)...)
	}
	return &port.VisionResult{
		Markdown:        strings.TrimSpace(markdown.String()),
		UnreadablePages: unreadable,
		Provider:        d.Name(),
	}, nil
}

/* convertChunk 下载一批页面并以一条用户消息提交模型 */
func (d *DeepSeek) convertChunk(ctx context.Context, pages []port.VisionPage) (string, error) {
	parts := make([]contentPart, 0, len(pages)*2+1)
	parts = append(parts, contentPart{
		Type: "text",
		Text: fmt.Sprintf("请按顺序转写以下 %d 页图片：", len(pages)),
	})
	for _, page := range pages {
		data, contentType, err := d.fetchPage(ctx, page)
		if err != nil {
			return "", err
		}
		parts = append(parts, contentPart{Type: "text", Text: fmt.Sprintf("第 %d 页：", page.Index)})
		parts = append(parts, contentPart{
			Type:     "image_url",
			ImageURL: &imageURL{URL: "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)},
		})
	}

	payload := chatRequest{
		Model: d.model,
		Messages: []chatMessage{
			{Role: "system", Content: layoutPrompt},
			{Role: "user", Content: parts},
		},
		Stream: false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: 构造请求失败: %w", port.ErrProviderUnavailable, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%w: 构造请求失败: %w", port.ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: 调用模型失败: %w", port.ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChatResponseBytes))
	if err != nil {
		return "", fmt.Errorf("%w: 读取响应失败: %w", port.ErrProviderUnavailable, err)
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil && resp.StatusCode == http.StatusOK {
		return "", fmt.Errorf("%w: 响应不是合法 JSON: %w", port.ErrProviderUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", d.mapError(resp.StatusCode, errorMessage(parsed, body))
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("%w: 响应缺少 choices", port.ErrProviderUnavailable)
	}
	content := normalizeMarkdown(parsed.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("%w: 模型返回空正文", port.ErrImageUnreadable)
	}
	return content, nil
}

/*
 * fetchPage 下载预签名页面图片并校验类型。
 * 下载失败视为供应商不可用；类型不受支持或体积异常视为内容不支持，便于任务失败码区分。
 */
func (d *DeepSeek) fetchPage(ctx context.Context, page port.VisionPage) ([]byte, string, error) {
	if strings.TrimSpace(page.URL) == "" {
		return nil, "", fmt.Errorf("%w: 第 %d 页缺少访问地址", port.ErrProviderUnavailable, page.Index)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page.URL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: 第 %d 页地址非法: %w", port.ErrProviderUnavailable, page.Index, err)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: 下载第 %d 页失败: %w", port.ErrProviderUnavailable, page.Index, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: 下载第 %d 页返回 %d", port.ErrProviderUnavailable, page.Index, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: 读取第 %d 页失败: %w", port.ErrProviderUnavailable, page.Index, err)
	}
	if len(data) > maxPageBytes {
		return nil, "", fmt.Errorf("%w: 第 %d 页体积超过上限", port.ErrContentUnsupported, page.Index)
	}
	contentType := imageContentType(resp.Header.Get("Content-Type"), data)
	if contentType == "" {
		return nil, "", fmt.Errorf("%w: 第 %d 页不是受支持的图片", port.ErrContentUnsupported, page.Index)
	}
	return data, contentType, nil
}

/*
 * imageContentType 归一图片类型：优先响应头，缺失或不可信时按字节嗅探。
 * 仅接受上传链路允许的 JPEG/PNG/WebP，其余一律拒绝。
 */
func imageContentType(header string, data []byte) string {
	if header != "" {
		if mediaType, _, err := mime.ParseMediaType(header); err == nil && supportedImageType(mediaType) {
			return mediaType
		}
	}
	detected := http.DetectContentType(data)
	mediaType, _, err := mime.ParseMediaType(detected)
	if err == nil && supportedImageType(mediaType) {
		return mediaType
	}
	return ""
}

func supportedImageType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

/* mapError 把 HTTP 状态映射为端口哨兵错误，避免供应商原始错误向上传递 */
func (d *DeepSeek) mapError(status int, message string) error {
	message = strings.TrimSpace(message)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: 鉴权失败（%d）：%s", port.ErrProviderNotConfigured, status, message)
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge ||
		status == http.StatusUnprocessableEntity:
		lower := strings.ToLower(message)
		if strings.Contains(lower, "image") || strings.Contains(lower, "vision") || strings.Contains(lower, "content") {
			return fmt.Errorf("%w: 图片无法识别（%d）：%s", port.ErrImageUnreadable, status, message)
		}
		return fmt.Errorf("%w: 请求被拒绝（%d）：%s", port.ErrProviderUnavailable, status, message)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: 触发供应商限流（%d）：%s", port.ErrProviderUnavailable, status, message)
	default:
		return fmt.Errorf("%w: 供应商返回 %d：%s", port.ErrProviderUnavailable, status, message)
	}
}

/* errorMessage 提取供应商错误文本，JSON 解析失败时回落到原始响应体 */
func errorMessage(parsed chatResponse, body []byte) string {
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return parsed.Error.Message
	}
	return string(body)
}

/*
 * normalizeMarkdown 清洗模型输出：去掉整体包裹的三反引号代码块与首尾空白。
 * 只处理「整篇被包裹」的情况，正文内部合法的代码块保持不变。
 */
func normalizeMarkdown(raw string) string {
	text := strings.TrimSpace(raw)
	if len(text) <= 6 || !strings.HasPrefix(text, "```") || !strings.HasSuffix(text, "```") {
		return text
	}
	firstBreak := strings.IndexByte(text, '\n')
	if firstBreak <= 0 {
		return text
	}
	return strings.TrimSpace(text[firstBreak+1 : len(text)-3])
}

/* findUnreadable 从正文占位提取未识别页码，保持首次出现的顺序 */
func findUnreadable(markdown string) []int {
	matches := unreadablePattern.FindAllStringSubmatch(markdown, -1)
	indexes := make([]int, 0, len(matches))
	for _, match := range matches {
		index, err := strconv.Atoi(match[1])
		if err != nil || index <= 0 {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

/* Health 只校验配置：远程连通性由转换任务的真实调用暴露，避免就绪探针产生计费 */
func (d *DeepSeek) Health(ctx context.Context) error {
	if strings.TrimSpace(d.apiKey) == "" {
		return port.ErrProviderNotConfigured
	}
	return nil
}
