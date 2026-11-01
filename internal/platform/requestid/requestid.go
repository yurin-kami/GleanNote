/*
 * Package requestid 承载请求 ID 的生成与上下文传递。
 *
 * 职责：生成符合契约的请求 ID，并在 context 中透传给日志与错误体。
 * 不负责：HTTP 头读写（由传输层中间件完成）。
 * 依赖：platform/random。
 * 参考：docs/API.md#23-请求与响应头、docs/ARCHITECTURE.md#811-日志指标与追踪
 */
package requestid

import (
	"context"

	"github.com/gleannote/gleannote/internal/platform/random"
)

/* contextKey 使用未导出类型，避免与其他包的上下文键冲突 */
type contextKey struct{}

/* With 把请求 ID 写入上下文；空串直接返回原上下文，保持调用方语义 */
func With(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, id)
}

/* From 读取请求 ID，未注入时返回空串 */
func From(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

/* New 生成形如 req_ 前缀的高熵请求 ID */
func New() (string, error) {
	suffix, err := random.Token(12)
	if err != nil {
		return "", err
	}
	return "req_" + suffix, nil
}

/*
 * Valid 判断客户端自带的请求 ID 是否可接受。
 * 约束：8–64 位可见 ASCII，禁止空白与换行，防止日志注入。
 */
func Valid(raw string) bool {
	if len(raw) < 8 || len(raw) > 64 {
		return false
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] < 0x21 || raw[index] > 0x7E {
			return false
		}
	}
	return true
}
