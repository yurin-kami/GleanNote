/*
 * Package bizdate 统一业务日期口径：一律按北京时间（UTC+8）计算。
 *
 * 职责：把任意时刻换算为业务日、当日起点、次日零点（额度重置点）。
 * 不负责：展示层格式化、数据库时区设置。
 * 依赖：仅标准库。
 * 参考：docs/ARCHITECTURE.md#85-时间时区与日期、docs/API.md#517-用户与额度
 */
package bizdate

import "time"

/*
 * Location 是业务时区。
 * 中国自 1991 年起不再使用夏令时，固定 +8 偏移与 IANA Asia/Shanghai 在业务日期上等价，
 * 固定偏移同时避免依赖宿主机的 tzdata 文件。
 */
var Location = time.FixedZone("Asia/Shanghai", 8*60*60)

/* Day 返回时刻所属的业务日期，格式固定为 YYYY-MM-DD */
func Day(at time.Time) string {
	return at.In(Location).Format("2006-01-02")
}

/* StartOfDay 返回该时刻所属业务日的北京时间零点，结果以 UTC 表示 */
func StartOfDay(at time.Time) time.Time {
	local := at.In(Location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Location).UTC()
}

/*
 * ResetAt 返回下一个业务日的北京时间零点（UTC 表示）。
 * 该值用于额度重置与前端展示，不得用「当前时间 + 24h」替代。
 */
func ResetAt(at time.Time) time.Time {
	return StartOfDay(at).AddDate(0, 0, 1)
}

/* DayStart 按业务日期字符串（YYYY-MM-DD）返回当日零点，解析失败返回零值 */
func DayStart(day string) (time.Time, bool) {
	parsed, err := time.ParseInLocation("2006-01-02", day, Location)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
