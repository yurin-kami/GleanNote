/*
 * 视觉 Fake 的单元测试：锁定确定性输出、故障映射与就绪口径。
 * 测试同样遵守块注释规则，参见 docs/CONVENTIONS.md。
 */
package vision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* TestFakeConvertDeterministic 校验偶数页被标记为未识别且输出可复现 */
func TestFakeConvertDeterministic(t *testing.T) {
	fake := NewFake(0, "")
	pages := []port.VisionPage{{Index: 1}, {Index: 2}, {Index: 3}}

	result, err := fake.Convert(context.Background(), pages)
	if err != nil {
		t.Fatalf("Convert 返回错误: %v", err)
	}
	if result.Provider != "fake" {
		t.Fatalf("Provider = %q，期望 fake", result.Provider)
	}
	if len(result.UnreadablePages) != 1 || result.UnreadablePages[0] != 2 {
		t.Fatalf("UnreadablePages = %v，期望 [2]", result.UnreadablePages)
	}
	if !strings.Contains(result.Markdown, "（第 2 页未识别到文字）") {
		t.Fatalf("Markdown 缺少未识别占位: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "## 第 3 页") {
		t.Fatalf("Markdown 缺少第 3 页标题: %q", result.Markdown)
	}
}

/* TestFakeConvertFailureMapping 校验故障注入映射为端口哨兵错误 */
func TestFakeConvertFailureMapping(t *testing.T) {
	cases := map[string]error{
		"unavailable": port.ErrProviderUnavailable,
		"unreadable":  port.ErrImageUnreadable,
		"unsupported": port.ErrContentUnsupported,
	}
	for failure, want := range cases {
		fake := NewFake(0, failure)
		_, err := fake.Convert(context.Background(), []port.VisionPage{{Index: 1}})
		if !errors.Is(err, want) {
			t.Fatalf("failure=%s 返回 %v，期望 %v", failure, err, want)
		}
	}
}

/* TestFakeConvertRespectsCancel 校验延迟期间取消能立即返回 */
func TestFakeConvertRespectsCancel(t *testing.T) {
	fake := NewFake(50*time.Millisecond, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fake.Convert(ctx, []port.VisionPage{{Index: 1}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("返回 %v，期望 context.Canceled", err)
	}
}

/* TestFakeHealth 校验只有 unavailable 影响就绪 */
func TestFakeHealth(t *testing.T) {
	if err := NewFake(0, "").Health(context.Background()); err != nil {
		t.Fatalf("正常态 Health 返回错误: %v", err)
	}
	if err := NewFake(0, "unreadable").Health(context.Background()); err != nil {
		t.Fatalf("unreadable 不应影响就绪: %v", err)
	}
	if err := NewFake(0, "unavailable").Health(context.Background()); !errors.Is(err, port.ErrProviderUnavailable) {
		t.Fatalf("unavailable 返回 %v，期望 ErrProviderUnavailable", err)
	}
}

/* TestNewRejectsUnknownProvider 校验未实现的供应商必须在装配期失败 */
func TestNewRejectsUnknownProvider(t *testing.T) {
	provider, err := New(Options{Name: "vendor-x"})
	if provider != nil {
		t.Fatalf("未实现供应商返回了非空提供者")
	}
	if !errors.Is(err, port.ErrProviderNotConfigured) {
		t.Fatalf("返回 %v，期望 ErrProviderNotConfigured", err)
	}
}
