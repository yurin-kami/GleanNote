/* 测试业务日期口径：跨 UTC 日界、北京零点与次日重置点 */
package bizdate

import (
	"testing"
	"time"
)

/* UTC 16:00 之后即进入北京时间的次日，业务日必须随之切换 */
func TestDayCrossesUTCMidnight(t *testing.T) {
	before := time.Date(2026, 10, 28, 15, 59, 59, 0, time.UTC)
	after := time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)

	if got := Day(before); got != "2026-10-28" {
		t.Fatalf("UTC 15:59 期望 2026-10-28，实际 %s", got)
	}
	if got := Day(after); got != "2026-10-29" {
		t.Fatalf("UTC 16:00 期望 2026-10-29，实际 %s", got)
	}
}

/* 业务日起点必须是北京时间 00:00，即 UTC 前一日 16:00 */
func TestStartOfDayAndResetAt(t *testing.T) {
	at := time.Date(2026, 10, 28, 20, 30, 0, 0, time.UTC)

	start := StartOfDay(at)
	want := time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)
	if !start.Equal(want) {
		t.Fatalf("起点期望 %s，实际 %s", want, start)
	}

	reset := ResetAt(at)
	wantReset := time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)
	if !reset.Equal(wantReset) {
		t.Fatalf("重置点期望 %s，实际 %s", wantReset, reset)
	}
}

/* 日期字符串解析必须与 Day 互为逆运算，非法输入返回 false */
func TestDayStartRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	start, ok := DayStart(Day(at))
	if !ok {
		t.Fatal("合法日期解析失败")
	}
	if !start.Equal(StartOfDay(at)) {
		t.Fatalf("往返结果不一致: %s vs %s", start, StartOfDay(at))
	}

	if _, ok := DayStart("2026-13-01"); ok {
		t.Fatal("非法日期应当返回 false")
	}
}
