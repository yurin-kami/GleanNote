/* 测试验证码摘要：确定性、场景绑定、密钥隔离与密钥长度校验 */
package codehash

import (
	"strings"
	"testing"
)

/* 摘要必须确定性，且输出为 64 位十六进制字符串 */
func TestDigestDeterministic(t *testing.T) {
	hasher, err := New([]byte(strings.Repeat("s", 32)))
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	first := hasher.Digest("a@example.com", "REGISTER", "123456")
	second := hasher.Digest("a@example.com", "REGISTER", "123456")
	if first != second {
		t.Fatal("同一输入摘要应一致")
	}
	if len(first) != 64 {
		t.Fatalf("摘要长度应为 64，实际 %d", len(first))
	}
}

/* 邮箱、用途、验证码与密钥任一变化都必须改变摘要，防止跨场景复用 */
func TestDigestBindsContext(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	hasher, _ := New(secret)
	base := hasher.Digest("a@example.com", "REGISTER", "123456")

	cases := []string{
		hasher.Digest("b@example.com", "REGISTER", "123456"),
		hasher.Digest("a@example.com", "RESET_PASSWORD", "123456"),
		hasher.Digest("a@example.com", "REGISTER", "654321"),
	}
	other, _ := New([]byte(strings.Repeat("t", 32)))
	cases = append(cases, other.Digest("a@example.com", "REGISTER", "123456"))

	for _, value := range cases {
		if value == base {
			t.Fatal("上下文变化后摘要不应相同")
		}
	}
}

/* 密钥不足 32 字节必须拒绝构造 */
func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("短密钥应构造失败")
	}
	if _, err := New(nil); err == nil {
		t.Fatal("空密钥应构造失败")
	}
}
