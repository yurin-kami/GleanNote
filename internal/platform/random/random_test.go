/* 测试随机令牌与数字验证码的格式、长度与唯一性 */
package random

import (
	"strings"
	"testing"
)

/* 令牌必须无填充、URL 安全，且相同长度下不重复 */
func TestTokenFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for index := 0; index < 64; index++ {
		token, err := Token(32)
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if strings.ContainsAny(token, "=+/") {
			t.Fatalf("令牌含填充或非 URL 安全字符: %s", token)
		}
		if _, exists := seen[token]; exists {
			t.Fatalf("令牌重复: %s", token)
		}
		seen[token] = struct{}{}
	}
}

/* 验证码必须是固定长度的纯数字 */
func TestDigits(t *testing.T) {
	code, err := Digits(6)
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("期望 6 位，实际 %d 位: %s", len(code), code)
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			t.Fatalf("含非数字字符: %s", code)
		}
	}
}

/* 非法参数必须返回错误而不是静默生成空串 */
func TestInvalidArguments(t *testing.T) {
	if _, err := Token(0); err == nil {
		t.Fatal("Token(0) 应返回错误")
	}
	if _, err := Digits(-1); err == nil {
		t.Fatal("Digits(-1) 应返回错误")
	}
}
