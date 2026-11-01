/* 测试分页令牌：往返一致、防篡改、作用域隔离、过期与弱密钥拒绝 */
package pagetoken

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/platform/clock"
)

/* 构造测试编解码器与固定时钟，供各用例共享 */
func newTestCodec(t *testing.T) (*Codec, *clock.Fixed) {
	t.Helper()
	fixed := clock.NewFixed(time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC))
	codec, err := NewCodec([]byte("0123456789abcdef0123456789abcdef"), time.Hour, fixed)
	if err != nil {
		t.Fatalf("构造编解码器失败: %v", err)
	}
	return codec, fixed
}

/* 编码后解码必须还原排序键与资源 ID */
func TestEncodeDecodeRoundTrip(t *testing.T) {
	codec, _ := newTestCodec(t)
	token, err := codec.Encode("notes", "2026-10-28T06:00:00Z", "018f2c1e-8a3b-7c4d-9e10-2b3c4d5e6f70")
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	decoded, err := codec.Decode("notes", token)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if decoded.SortKey != "2026-10-28T06:00:00Z" {
		t.Fatalf("排序键不一致: %s", decoded.SortKey)
	}
	if decoded.ID != "018f2c1e-8a3b-7c4d-9e10-2b3c4d5e6f70" {
		t.Fatalf("ID 不一致: %s", decoded.ID)
	}
}

/* 篡改载荷或签名都必须判定为非法 */
func TestTamperedTokenRejected(t *testing.T) {
	codec, _ := newTestCodec(t)
	token, err := codec.Encode("notes", "key", "id")
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	parts := strings.Split(token, ".")
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("解码载荷失败: %v", err)
	}
	tampered := base64.RawURLEncoding.EncodeToString(append(body[:len(body)-1], 'X'))
	if _, err := codec.Decode("notes", tampered+"."+parts[1]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("篡改载荷应返回 ErrInvalid，实际 %v", err)
	}
	if _, err := codec.Decode("notes", parts[0]+".AAAA"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("篡改签名应返回 ErrInvalid，实际 %v", err)
	}
	if _, err := codec.Decode("notes", "not-a-token"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("格式错误应返回 ErrInvalid，实际 %v", err)
	}
}

/* 跨端点复用令牌必须被拒绝 */
func TestScopeIsolation(t *testing.T) {
	codec, _ := newTestCodec(t)
	token, err := codec.Encode("notes", "key", "id")
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if _, err := codec.Decode("trash", token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("跨作用域应返回 ErrInvalid，实际 %v", err)
	}
}

/* 超过有效期后解码必须返回 ErrExpired，且过期不改变签名合法性判断 */
func TestExpiry(t *testing.T) {
	codec, fixed := newTestCodec(t)
	token, err := codec.Encode("notes", "key", "id")
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	fixed.Advance(59 * time.Minute)
	if _, err := codec.Decode("notes", token); err != nil {
		t.Fatalf("有效期内应可解码: %v", err)
	}

	fixed.Advance(2 * time.Minute)
	if _, err := codec.Decode("notes", token); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期应返回 ErrExpired，实际 %v", err)
	}
}

/* 构造参数必须拒绝弱密钥、非正有效期与缺失时钟 */
func TestNewCodecValidation(t *testing.T) {
	now := clock.NewFixed(time.Now())
	if _, err := NewCodec([]byte("short"), time.Hour, now); err == nil {
		t.Fatal("弱密钥应构造失败")
	}
	if _, err := NewCodec([]byte("0123456789abcdef0123456789abcdef"), 0, now); err == nil {
		t.Fatal("零有效期应构造失败")
	}
	if _, err := NewCodec([]byte("0123456789abcdef0123456789abcdef"), time.Hour, nil); err == nil {
		t.Fatal("缺失时钟应构造失败")
	}
}
