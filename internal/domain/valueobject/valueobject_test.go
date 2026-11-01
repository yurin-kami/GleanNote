/* 测试邮箱、密码与昵称的归一化和边界：合法输入、越界长度、字符集与缺省值 */
package valueobject

import (
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 邮箱必须去空白并小写化，LocalPart 依赖归一化结果 */
func TestParseEmailNormalizes(t *testing.T) {
	email, err := ParseEmail("  Alice.Wang@Example.COM ")
	if err != nil {
		t.Fatalf("合法邮箱解析失败: %v", err)
	}
	if email.String() != "alice.wang@example.com" {
		t.Fatalf("归一化结果不符: %s", email.String())
	}
	if email.LocalPart() != "alice.wang" {
		t.Fatalf("@ 前部分不符: %s", email.LocalPart())
	}
}

/* 各类非法邮箱必须返回 VALIDATION_FAILED，不允许静默接受 */
func TestParseEmailRejects(t *testing.T) {
	cases := []string{
		"",
		"alice",
		"alice@",
		"alice@example",
		"alice@.com",
		"Alice <alice@example.com>",
		strings.Repeat("a", 245) + "@example.com",
	}
	for _, input := range cases {
		if _, err := ParseEmail(input); !errs.IsValidation(err) {
			t.Fatalf("输入 %q 应校验失败，实际 %v", input, err)
		}
	}
}

/* 密码长度按字符计，边界值必须被接受，越界必须被拒绝 */
func TestPasswordLengthBoundary(t *testing.T) {
	if err := CheckPasswordPolicy("abc12345"); err != nil {
		t.Fatalf("8 位应合法: %v", err)
	}
	if err := CheckPasswordPolicy("abcdefgh1"); err != nil {
		t.Fatalf("9 位应合法: %v", err)
	}
	if err := CheckPasswordPolicy("a1"); !errs.IsValidation(err) {
		t.Fatalf("过短应校验失败: %v", err)
	}
	if err := CheckPasswordPolicy(strings.Repeat("a1", 17)); !errs.IsValidation(err) {
		t.Fatalf("超过 32 位应校验失败: %v", err)
	}
}

/* 必须同时包含字母与数字，纯数字或纯字母都不满足策略 */
func TestPasswordRequiresLetterAndDigit(t *testing.T) {
	if err := CheckPasswordPolicy("12345678"); !errs.IsValidation(err) {
		t.Fatalf("纯数字应失败: %v", err)
	}
	if err := CheckPasswordPolicy("abcdefgh"); !errs.IsValidation(err) {
		t.Fatalf("纯字母应失败: %v", err)
	}
	if err := CheckPasswordPolicy("abcd1234\n"); !errs.IsValidation(err) {
		t.Fatalf("含换行应失败: %v", err)
	}
}

/* 昵称必须去空白、限长且拒绝换行 */
func TestNicknameRules(t *testing.T) {
	normalized, err := NormalizeNickname("  小张  ")
	if err != nil || normalized != "小张" {
		t.Fatalf("归一化失败: %q %v", normalized, err)
	}

	if _, err := NormalizeNickname("   "); !errs.IsValidation(err) {
		t.Fatalf("空白应失败: %v", err)
	}
	if _, err := NormalizeNickname(strings.Repeat("字", 21)); !errs.IsValidation(err) {
		t.Fatalf("超长应失败: %v", err)
	}
	if _, err := NormalizeNickname("小\n张"); !errs.IsValidation(err) {
		t.Fatalf("含换行应失败: %v", err)
	}
}

/* 目录名称：长度边界、首尾空格、非法字符与控制字符 */
func TestFolderNameRules(t *testing.T) {
	name, err := ParseFolderName("线性代数")
	if err != nil || name.String() != "线性代数" {
		t.Fatalf("合法名称应通过: %q %v", name, err)
	}
	if _, err := ParseFolderName(strings.Repeat("字", MaxFolderNameLength)); err != nil {
		t.Fatalf("50 个字符应合法: %v", err)
	}

	invalid := []string{
		"",
		" 目录",
		"目录 ",
		strings.Repeat("字", MaxFolderNameLength+1),
		"a/b",
		"a\\b",
		"a:b",
		"a*b",
		"a?b",
		"a\"b",
		"a<b",
		"a>b",
		"a|b",
		"a\nb",
	}
	for _, input := range invalid {
		if _, err := ParseFolderName(input); !errs.IsValidation(err) {
			t.Fatalf("输入 %q 应校验失败，实际 %v", input, err)
		}
	}
}

/* 大小写不同的英文名应产生相同比较键，用于同级查重 */
func TestFolderNameKeyIsCaseInsensitive(t *testing.T) {
	upper, err := ParseFolderName("Notes")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	lower, err := ParseFolderName("notes")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if upper.Key() != lower.Key() {
		t.Fatalf("比较键应一致: %q vs %q", upper.Key(), lower.Key())
	}
}

/* 重名后缀必须放在长度上限内，且序号从「 (2)」开始 */
func TestFolderNameWithDuplicateSuffix(t *testing.T) {
	name, err := ParseFolderName("线性代数")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := name.WithDuplicateSuffix(1).String(); got != "线性代数 (2)" {
		t.Fatalf("首个后缀不符: %s", got)
	}
	if got := name.WithDuplicateSuffix(2).String(); got != "线性代数 (3)" {
		t.Fatalf("第二个后缀不符: %s", got)
	}

	long, err := ParseFolderName(strings.Repeat("字", MaxFolderNameLength))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	suffixed := long.WithDuplicateSuffix(9)
	if length := len([]rune(suffixed.String())); length > MaxFolderNameLength {
		t.Fatalf("加后缀后长度 = %d，应不超过 %d", length, MaxFolderNameLength)
	}
}

/* 缺省昵称取邮箱本地部分并按字符截断，空本地部分回落为「用户」 */
func TestDefaultNickname(t *testing.T) {
	email, err := ParseEmail(strings.Repeat("a", 30) + "@example.com")
	if err != nil {
		t.Fatalf("邮箱解析失败: %v", err)
	}
	nickname := DefaultNickname(email)
	if len([]rune(nickname)) != MaxNicknameLength {
		t.Fatalf("应按字符截断到 20，实际 %d", len([]rune(nickname)))
	}

	short, err := ParseEmail("zhangsan@example.com")
	if err != nil {
		t.Fatalf("邮箱解析失败: %v", err)
	}
	if got := DefaultNickname(short); got != "zhangsan" {
		t.Fatalf("缺省昵称不符: %s", got)
	}
}
