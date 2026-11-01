/* 刷新令牌实体：承担轮换、吊销与重放检测的状态判定 */
package entity

import "time"

/*
 * RefreshToken 只保存令牌哈希，原始令牌仅出现在响应中。
 * DeviceID 用于「吊销该设备全部令牌」与重放后的按设备吊销。
 */
type RefreshToken struct {
	ID         string
	UserID     string
	TokenHash  string
	DeviceID   string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy string
	CreatedAt  time.Time
}

/* Usable 判断令牌当前是否可用于换取新令牌 */
func (t *RefreshToken) Usable(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

/* Rotated 表示令牌已因正常轮换被替换，再次出现即视为重放攻击 */
func (t *RefreshToken) Rotated() bool {
	return t.ReplacedBy != ""
}

/*
 * Revoke 吊销令牌。
 * replacedBy 为空表示登出或批量吊销；非空表示轮换后的新令牌 ID。
 * 方法幂等，重复吊销不改变首次的记录。
 */
func (t *RefreshToken) Revoke(now time.Time, replacedBy string) {
	if t.RevokedAt != nil {
		return
	}
	revoked := now
	t.RevokedAt = &revoked
	t.ReplacedBy = replacedBy
}
