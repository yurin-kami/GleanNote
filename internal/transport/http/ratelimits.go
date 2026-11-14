/*
 * 限流配额的传输层载体。
 *
 * 默认值即生产口径（与 docs/ARCHITECTURE.md#87-限流与防滥用 一致）；
 * 允许通过 GN_RATE_* 环境变量覆盖，用于压测环境放开限流，避免为了测吞吐改代码。
 * 约定：零值表示"未注入"，由 normalize 回填默认值，保证既有测试与装配方式不受影响。
 */
package httptransport

import "time"

/* RateLimits 是各类限流的配额与窗口；窗口固定，只有配额可覆盖 */
type RateLimits struct {
	GlobalPerMinute      int
	LoginPerMinute       int
	WritePerMinute       int
	UploadPerHour        int
	PublicSharePerMinute int
}

/* 默认配额，与改动前的常量保持一致 */
const (
	defaultGlobalPerMinute      = 600
	defaultLoginPerMinute       = 10
	defaultWritePerMinute       = 120
	defaultUploadPerHour        = 100
	defaultPublicSharePerMinute = 60
)

/* normalize 用默认值填充未注入的字段；负数同样按未注入处理，避免出现"永不放行" */
func (r RateLimits) normalize() RateLimits {
	if r.GlobalPerMinute <= 0 {
		r.GlobalPerMinute = defaultGlobalPerMinute
	}
	if r.LoginPerMinute <= 0 {
		r.LoginPerMinute = defaultLoginPerMinute
	}
	if r.WritePerMinute <= 0 {
		r.WritePerMinute = defaultWritePerMinute
	}
	if r.UploadPerHour <= 0 {
		r.UploadPerHour = defaultUploadPerHour
	}
	if r.PublicSharePerMinute <= 0 {
		r.PublicSharePerMinute = defaultPublicSharePerMinute
	}
	return r
}

/* 各限流窗口；压测时只放大配额、保持窗口不变，口径与线上一致 */
const (
	globalRateWindow      = time.Minute
	loginRateWindow       = time.Minute
	writeRateWindow       = time.Minute
	uploadRateWindow      = time.Hour
	publicShareRateWindow = time.Minute
)
