/* 测试请求 ID 的生成格式、合法性判定与上下文透传 */
package requestid

import (
	"context"
	"strings"
	"testing"
)

/* 生成的 ID 必须带 req_ 前缀且通过合法性校验 */
func TestNewRequestID(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if !strings.HasPrefix(id, "req_") {
		t.Fatalf("缺少前缀: %s", id)
	}
	if !Valid(id) {
		t.Fatalf("生成结果应合法: %s", id)
	}
}

/* 长度、空白与控制字符都必须被拒绝，防止日志注入 */
func TestValidRejectsIllegalInput(t *testing.T) {
	cases := map[string]bool{
		"req_0123456789ab":      true,
		"short":                 false,
		"req id with space":     false,
		"req_id\ninject":        false,
		strings.Repeat("a", 65): false,
	}
	for input, want := range cases {
		if got := Valid(input); got != want {
			t.Fatalf("Valid(%q) 期望 %v，实际 %v", input, want, got)
		}
	}
}

/* 上下文透传必须可读回；空串不写入上下文 */
func TestContextRoundTrip(t *testing.T) {
	ctx := With(context.Background(), "req_abcdef123456")
	if got := From(ctx); got != "req_abcdef123456" {
		t.Fatalf("读回不一致: %s", got)
	}

	if got := From(With(context.Background(), "")); got != "" {
		t.Fatalf("空串不应写入: %s", got)
	}
	if got := From(context.Background()); got != "" {
		t.Fatalf("未注入时应为空: %s", got)
	}
}
