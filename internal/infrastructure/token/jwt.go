/*
 * Package token 签发与校验 EdDSA（Ed25519）签名的最小访问令牌。
 *
 * 职责：把用户、设备、令牌 ID 与有效期封装为 JWT，并提供严格算法的校验。
 * 不负责：刷新令牌（不透明串由用例生成）、吊销黑名单存储。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权、ADR-007
 */
package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* AccessTokenTTL 是访问令牌有效期，契约要求 15 分钟 */
const AccessTokenTTL = 15 * time.Minute

/* issuer 是 JWT 的 iss 声明，校验时强制匹配 */
const issuer = "gleannote"

/* claims 在标准声明之外只增加设备标识，载荷保持最小 */
type claims struct {
	DeviceID string `json:"deviceId"`
	jwt.RegisteredClaims
}

/* Ed25519 同时实现签发与校验端口；私钥不可导出 */
type Ed25519 struct {
	private ed25519.PrivateKey
}

/*
 * NewEd25519 由 32 字节种子派生密钥。
 * seed 为空时生成一次性密钥，仅用于开发态；生产必须注入固定种子以支持多实例校验。
 */
func NewEd25519(seed []byte) (*Ed25519, error) {
	switch len(seed) {
	case 0:
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("生成 Ed25519 密钥: %w", err)
		}
		return &Ed25519{private: private}, nil
	case ed25519.SeedSize:
		return &Ed25519{private: ed25519.NewKeyFromSeed(seed)}, nil
	default:
		return nil, fmt.Errorf("Ed25519 种子长度必须为 0 或 %d，实际 %d", ed25519.SeedSize, len(seed))
	}
}

/*
 * Issue 签发访问令牌。
 * 约束：userID 与 tokenID 必填；设备标识可为空，空值时按 IP 维度管理登录态。
 */
func (s *Ed25519) Issue(userID, deviceID, tokenID string, now time.Time) (string, time.Time, error) {
	if userID == "" || tokenID == "" {
		return "", time.Time{}, errors.New("token: 签发缺少用户或令牌 ID")
	}
	expiresAt := now.Add(AccessTokenTTL)
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		DeviceID: deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   userID,
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})
	signed, err := token.SignedString(s.private)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: 签名失败: %w", err)
	}
	return signed, expiresAt, nil
}

/*
 * Verify 校验签名、签发者、有效期与必填声明。
 * 只接受 EdDSA 算法，防止 alg=none 与算法混淆攻击（RFC 8725）。
 */
func (s *Ed25519) Verify(raw string, now time.Time) (port.AccessClaims, error) {
	if raw == "" {
		return port.AccessClaims{}, port.ErrTokenInvalid
	}
	parsed := &claims{}
	verified, err := jwt.ParseWithClaims(raw, parsed, func(t *jwt.Token) (any, error) {
		return s.private.Public(), nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return port.AccessClaims{}, port.ErrTokenExpired
		}
		return port.AccessClaims{}, port.ErrTokenInvalid
	}
	if !verified.Valid || parsed.Subject == "" || parsed.ID == "" || parsed.ExpiresAt == nil {
		return port.AccessClaims{}, port.ErrTokenInvalid
	}
	return port.AccessClaims{
		UserID:    parsed.Subject,
		DeviceID:  parsed.DeviceID,
		TokenID:   parsed.ID,
		ExpiresAt: parsed.ExpiresAt.Time.UTC(),
	}, nil
}
