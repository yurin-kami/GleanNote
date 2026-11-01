/*
 * Package codehash 用服务端密钥对邮箱验证码做 HMAC 摘要。
 *
 * 职责：把 6 位数字验证码、邮箱与用途绑定为不可逆摘要，数据库不落明文。
 * 不负责：验证码生成、有效期与错误次数（见 entity.EmailCode）。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权
 */
package codehash

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

/* minSecretLength 防止使用过短的服务端密钥导致离线暴力破解 */
const minSecretLength = 32

/* Hasher 是 port.CodeHasher 的实现，构造后无状态且并发安全 */
type Hasher struct {
	secret []byte
}

/* New 复制并校验密钥；密钥过短直接拒绝启动 */
func New(secret []byte) (*Hasher, error) {
	if len(secret) < minSecretLength {
		return nil, errors.New("验证码摘要密钥至少 32 字节")
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &Hasher{secret: copied}, nil
}

/*
 * Digest 计算 HMAC-SHA256(part1 || 0x00 || part2 || ...)，逐段写入避免拼接歧义。
 * 验证码场景传入邮箱、用途与验证码；用途参与摘要使注册码无法用于重置密码。
 */
func (h *Hasher) Digest(parts ...string) string {
	mac := hmac.New(sha256.New, h.secret)
	for _, part := range parts {
		mac.Write([]byte(part))
		mac.Write([]byte{0x00})
	}
	return hex.EncodeToString(mac.Sum(nil))
}
