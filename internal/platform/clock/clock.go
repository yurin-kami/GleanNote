/*
 * Package clock 统一时间来源，避免业务代码直接调用 time.Now()。
 *
 * 职责：提供可注入的当前时间，统一按 UTC 返回。
 * 不负责：时区换算（见 platform/bizdate）、定时任务调度。
 * 依赖：仅标准库，可被任意层使用。
 * 参考：docs/ARCHITECTURE.md#85-时间时区与日期、docs/CONVENTIONS.md#31-通用 G-09
 */
package clock

import "time"

/*
 * Clock 是唯一允许获取「现在」的端口。
 * 实现必须返回 UTC 时间，业务层不得再做本地时区转换。
 */
type Clock interface {
	Now() time.Time
}

/* System 是生产环境实现，返回 UTC 的系统时钟 */
type System struct{}

/* Now 返回 UTC 当前时刻；截断单调时钟读数，避免跨进程比较出现偏差 */
func (System) Now() time.Time {
	return time.Now().UTC()
}

/*
 * Fixed 是可推进的测试时钟，供确定时间边界的用例使用。
 * 并发不安全，仅用于单测与开发态。
 */
type Fixed struct {
	now time.Time
}

/* NewFixed 以给定时刻初始化固定时钟，入参统一转换为 UTC */
func NewFixed(now time.Time) *Fixed {
	return &Fixed{now: now.UTC()}
}

/* Now 返回测试注入的当前时刻 */
func (f *Fixed) Now() time.Time {
	return f.now
}

/* Advance 向前推进时间，用于触发过期、锁定解除等分支 */
func (f *Fixed) Advance(d time.Duration) {
	f.now = f.now.Add(d)
}

/* Set 直接设置当前时刻，用于回拨到固定边界 */
func (f *Fixed) Set(now time.Time) {
	f.now = now.UTC()
}
