/*
 * 视觉识别能力的端口。
 *
 * 该能力按调用或 token 计费，开发与测试阶段只允许 fake 实现。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */

package port

import (
	"context"
	"errors"
)

/*
 * VisionPage 表示一页待识别图片。
 * URL 必须是服务端可访问的短期签名地址，客户端不得参与签名。
 */
type VisionPage struct {
	Index int
	URL   string
}

/*
 * VisionResult 是供应商返回的归一结果。
 * Markdown 为整篇正文；UnreadablePages 为未能识别文字的页码，从 1 开始。
 */
type VisionResult struct {
	Markdown        string
	UnreadablePages []int
	Provider        string
}

/*
 * VisionProvider 是「图片 → Markdown」付费能力的端口。
 * 实现方必须把供应商错误映射为本文件的哨兵错误，便于用例统一处理。
 */
type VisionProvider interface {
	Name() string
	Convert(ctx context.Context, pages []VisionPage) (*VisionResult, error)
	Health(ctx context.Context) error
}

/*
 * 视觉能力的归一错误。
 * 用例据此决定任务失败码与额度返还，不允许把供应商原始错误向上传递。
 */
var (
	ErrProviderUnavailable   = errors.New("provider unavailable")
	ErrProviderNotConfigured = errors.New("provider not configured")
	ErrImageUnreadable       = errors.New("image unreadable")
	ErrContentUnsupported    = errors.New("content unsupported")
)
