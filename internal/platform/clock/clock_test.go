/* 测试统一时间来源的行为：UTC 归一、可推进与可回拨，保证用例可控 */
package clock

import (
	"testing"
	"time"
)

/* 系统时钟必须返回 UTC 位置，避免序列化时出现 +08:00 偏移 */
func TestSystemClockReturnsUTC(t *testing.T) {
	now := System{}.Now()
	if now.Location() != time.UTC {
		t.Fatalf("期望 UTC，实际 %s", now.Location())
	}
}

/* 固定时钟的推进与回拨必须精确影响返回值 */
func TestFixedClockAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 10, 28, 6, 0, 0, 0, time.FixedZone("CST", 8*3600))
	fixed := NewFixed(start)
	if got := fixed.Now(); !got.Equal(start.UTC()) {
		t.Fatalf("初始化应归一到 UTC，实际 %s", got)
	}

	fixed.Advance(90 * time.Second)
	if got := fixed.Now(); !got.Equal(start.UTC().Add(90 * time.Second)) {
		t.Fatalf("推进后时间不符，实际 %s", got)
	}

	fixed.Set(start.Add(-time.Hour))
	if got := fixed.Now(); !got.Equal(start.UTC().Add(-time.Hour)) {
		t.Fatalf("回拨后时间不符，实际 %s", got)
	}
}
