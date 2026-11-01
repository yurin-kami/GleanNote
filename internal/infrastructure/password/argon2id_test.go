/* 测试 Argon2id 哈希：编码格式、随机盐、正确与错误口令、损坏编码串 */
package password

import (
	"context"
	"errors"
	"strings"
	"testing"
)

/* 同一明文两次哈希必须因随机盐而不同，但都能通过校验 */
func TestHashAndVerify(t *testing.T) {
	hasher := NewArgon2id()
	ctx := context.Background()

	first, err := hasher.Hash(ctx, "Passw0rd!")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	second, err := hasher.Hash(ctx, "Passw0rd!")
	if err != nil {
		t.Fatalf("再次哈希失败: %v", err)
	}
	if first == second {
		t.Fatal("随机盐未生效，两次结果相同")
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("编码格式不符: %s", first)
	}

	ok, err := hasher.Verify(ctx, first, "Passw0rd!")
	if err != nil || !ok {
		t.Fatalf("正确口令应校验通过: %v %v", ok, err)
	}
	ok, err = hasher.Verify(ctx, first, "passw0rd!")
	if err != nil || ok {
		t.Fatalf("错误口令应校验失败: %v %v", ok, err)
	}
}

/* 空明文与损坏编码串必须显式报错，不能静默放行 */
func TestHashVerifyRejects(t *testing.T) {
	hasher := NewArgon2id()
	ctx := context.Background()

	if _, err := hasher.Hash(ctx, ""); err == nil {
		t.Fatal("空明文应报错")
	}
	cases := []string{
		"",
		"plain-text",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$not-base64$not-base64",
	}
	for _, encoded := range cases {
		if _, err := hasher.Verify(ctx, encoded, "Passw0rd!"); !errors.Is(err, ErrMalformed) {
			t.Fatalf("编码串 %q 应返回 ErrMalformed，实际 %v", encoded, err)
		}
	}
}

/* 空口令校验必须返回 false 而非错误，避免把用户输入问题当成系统故障 */
func TestVerifyEmptyPlain(t *testing.T) {
	hasher := NewArgon2id()
	encoded, err := hasher.Hash(context.Background(), "Passw0rd!")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	ok, err := hasher.Verify(context.Background(), encoded, "")
	if err != nil || ok {
		t.Fatalf("空口令应返回 false, nil，实际 %v %v", ok, err)
	}
}
