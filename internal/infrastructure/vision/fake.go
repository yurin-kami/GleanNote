/*
 * Package vision 提供视觉识别能力的适配器与工厂。
 *
 * 工厂按名称选择实现：fake（离线开发与测试）与 deepseek（真实多模态识别）；
 * 未注册名称返回配置错误，禁止静默降级。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package vision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
)

/*
 * Options 汇总工厂参数。
 * Name 决定实现；其余字段只对相应实现生效（Fake 用 Latency/Failure，DeepSeek 用凭据与模型）。
 */
type Options struct {
	Name    string
	Latency time.Duration
	Failure string

	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

/*
 * Fake 是开发态视觉能力：确定性输出 + 可注入延迟与故障。
 * 它不访问网络，因此可在离线环境与单元测试中稳定复现。
 */
type Fake struct {
	latency time.Duration
	failure string
}

/* NewFake 创建开发态实现；latency 模拟供应商耗时，failure 用于故障演练 */
func NewFake(latency time.Duration, failure string) *Fake {
	return &Fake{
		latency: latency,
		failure: strings.TrimSpace(strings.ToLower(failure)),
	}
}

/*
 * New 按名称创建视觉提供者。
 * 未注册名称必须失败，禁止回落到 fake 掩盖配置错误。
 */
func New(opts Options) (port.VisionProvider, error) {
	switch strings.TrimSpace(strings.ToLower(opts.Name)) {
	case "", "fake":
		return NewFake(opts.Latency, opts.Failure), nil
	case "deepseek":
		return NewDeepSeek(opts)
	default:
		return nil, fmt.Errorf("%w: vision provider %q", port.ErrProviderNotConfigured, opts.Name)
	}
}

/* Name 返回提供者名称，用于启动日志与就绪探针展示 */
func (f *Fake) Name() string { return "fake" }

/*
 * Convert 模拟一次识图。
 * 先模拟延迟再注入失败，保证客户端能观察到「处理中」状态。
 */
func (f *Fake) Convert(ctx context.Context, pages []port.VisionPage) (*port.VisionResult, error) {
	if f.latency > 0 {
		select {
		case <-time.After(f.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch f.failure {
	case "unavailable":
		return nil, port.ErrProviderUnavailable
	case "unreadable":
		return nil, port.ErrImageUnreadable
	case "unsupported":
		return nil, port.ErrContentUnsupported
	default:
		return f.success(pages), nil
	}
}

/*
 * success 生成确定性结果：每页一个二级标题，偶数页视为未识别并插入占位。
 * 占位文案与 PRD FR-05 的「（第 N 页未识别到文字）」保持一致。
 */
func (f *Fake) success(pages []port.VisionPage) *port.VisionResult {
	var body strings.Builder
	unreadable := make([]int, 0, len(pages))
	for _, page := range pages {
		if page.Index%2 == 0 {
			unreadable = append(unreadable, page.Index)
			fmt.Fprintf(&body, "## 第 %d 页\n\n（第 %d 页未识别到文字）\n\n", page.Index, page.Index)
			continue
		}
		fmt.Fprintf(&body, "## 第 %d 页\n\n（Fake 提供器生成的第 %d 页示例内容）\n\n", page.Index, page.Index)
	}
	return &port.VisionResult{
		Markdown:        strings.TrimSpace(body.String()),
		UnreadablePages: unreadable,
		Provider:        "fake",
	}
}

/* Health 反映 unavailable 注入；其余故障不影响进程就绪 */
func (f *Fake) Health(ctx context.Context) error {
	if f.failure == "unavailable" {
		return port.ErrProviderUnavailable
	}
	return nil
}
