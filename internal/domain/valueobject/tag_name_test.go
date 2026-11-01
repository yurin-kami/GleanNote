/* 标签名称值对象测试：长度、空格与控制字符校验 */
package valueobject

import (
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* TestParseTagName 覆盖合法输入与各类非法输入 */
func TestParseTagName(t *testing.T) {
	valid, err := ParseTagName("考研")
	if err != nil {
		t.Fatalf("合法标签名被拒绝: %v", err)
	}
	if valid.String() != "考研" || valid.Key() != "考研" {
		t.Fatalf("标签名转换结果不符合预期: %q", valid)
	}

	/* 大小写无关比较键口径 */
	upper, err := ParseTagName("GRE")
	if err != nil {
		t.Fatalf("合法标签名被拒绝: %v", err)
	}
	if upper.Key() != "gre" {
		t.Fatalf("比较键 = %q，期望 gre", upper.Key())
	}

	cases := []struct {
		name string
		raw  string
	}{
		{"空名称", ""},
		{"首尾空格", " 考研 "},
		{"超过 20 字符", "一二三四五六七八九十一二三四五六七八九十一"},
		{"控制字符", "考\n研"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := ParseTagName(item.raw); !errs.IsValidation(err) {
				t.Fatalf("期望 VALIDATION_FAILED，实际 %v", err)
			}
		})
	}
}
