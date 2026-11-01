/* 测试访问令牌：声明还原、过期、篡改、算法混淆与密钥种子校验 */
package token

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* 签发返回的令牌必须可校验并还原全部声明 */
func TestIssueAndVerify(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	signer, err := NewEd25519([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	raw, expiresAt, err := signer.Issue("u-1", "device-1", "jti-1", now)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if !expiresAt.Equal(now.Add(AccessTokenTTL)) {
		t.Fatalf("过期时刻不符: %s", expiresAt)
	}

	claims, err := signer.Verify(raw, now)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if claims.UserID != "u-1" || claims.DeviceID != "device-1" || claims.TokenID != "jti-1" {
		t.Fatalf("声明还原不符: %+v", claims)
	}
}

/* 超过有效期必须返回 ErrTokenExpired */
func TestVerifyExpired(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	signer, _ := NewEd25519([]byte(strings.Repeat("s", 32)))
	raw, _, _ := signer.Issue("u-1", "", "jti-1", now)

	if _, err := signer.Verify(raw, now.Add(AccessTokenTTL)); !errors.Is(err, port.ErrTokenExpired) {
		t.Fatalf("过期应返回 ErrTokenExpired，实际 %v", err)
	}
}

/* 篡改载荷或使用 HS256 都必须返回 ErrTokenInvalid */
func TestVerifyRejectsTamperedAndWrongAlgorithm(t *testing.T) {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	signer, _ := NewEd25519([]byte(strings.Repeat("s", 32)))
	raw, _, _ := signer.Issue("u-1", "", "jti-1", now)

	parts := strings.Split(raw, ".")
	if _, err := signer.Verify(parts[0]+"."+parts[1]+".AAAA", now); !errors.Is(err, port.ErrTokenInvalid) {
		t.Fatalf("篡改签名应返回 ErrTokenInvalid，实际 %v", err)
	}

	hsToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": issuer, "sub": "u-1", "jti": "jti-1",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
	})
	signed, err := hsToken.SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("构造 HS256 令牌失败: %v", err)
	}
	if _, err := signer.Verify(signed, now); !errors.Is(err, port.ErrTokenInvalid) {
		t.Fatalf("算法混淆应返回 ErrTokenInvalid，实际 %v", err)
	}
}

/* 种子长度非法、签发缺少 ID 都必须报错 */
func TestGuards(t *testing.T) {
	now := time.Now().UTC()
	for _, seed := range [][]byte{nil, []byte(strings.Repeat("s", 32))} {
		if _, err := NewEd25519(seed); err != nil {
			t.Fatalf("种子长度 %d 应合法: %v", len(seed), err)
		}
	}
	if _, err := NewEd25519([]byte("short")); err == nil {
		t.Fatal("非法种子长度应报错")
	}

	signer, _ := NewEd25519([]byte(strings.Repeat("s", 32)))
	if _, _, err := signer.Issue("", "", "", now); err == nil {
		t.Fatal("缺少用户与令牌 ID 应报错")
	}
}
