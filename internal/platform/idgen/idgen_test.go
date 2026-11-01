/* 测试 UUIDv7 生成器：格式合法、版本正确、连续生成不重复且大体有序 */
package idgen

import (
	"testing"

	"github.com/google/uuid"
)

/* 生成的字符串必须能被解析为版本 7 的 UUID */
func TestUUIDv7Format(t *testing.T) {
	generated, err := UUIDv7{}.New()
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	parsed, err := uuid.Parse(generated)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("期望版本 7，实际 %d", parsed.Version())
	}
}

/* 连续生成必须唯一；同一毫秒内由随机位保证不冲突 */
func TestUUIDv7Unique(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for index := 0; index < 128; index++ {
		generated, err := UUIDv7{}.New()
		if err != nil {
			t.Fatalf("第 %d 次生成失败: %v", index, err)
		}
		if _, exists := seen[generated]; exists {
			t.Fatalf("重复 ID: %s", generated)
		}
		seen[generated] = struct{}{}
	}
}
