/* 限流配额解析：默认值、环境覆盖与非法值处理 */
package config

import "testing"

/* TestParsePositiveIntDefaultsAndOverrides 校验未配置取默认、配置后取覆盖值 */
func TestParsePositiveIntDefaultsAndOverrides(t *testing.T) {
	if value, err := parsePositiveInt("GN_RATE_TEST_UNSET", 600); err != nil || value != 600 {
		t.Fatalf("未配置 = (%d,%v)，期望 (600,nil)", value, err)
	}
	t.Setenv("GN_RATE_TEST_SET", "5000")
	if value, err := parsePositiveInt("GN_RATE_TEST_SET", 600); err != nil || value != 5000 {
		t.Fatalf("已配置 = (%d,%v)，期望 (5000,nil)", value, err)
	}
}

/* TestParsePositiveIntRejectsInvalid 校验非正整数必须启动失败，避免全部请求被限流 */
func TestParsePositiveIntRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"0", "-5", "abc"} {
		t.Setenv("GN_RATE_TEST_BAD", raw)
		if _, err := parsePositiveInt("GN_RATE_TEST_BAD", 600); err == nil {
			t.Fatalf("%q 应返回错误", raw)
		}
	}
}

/* TestLoadReadsRateLimits 校验 Load 读取 GN_RATE_* 且其余保持默认 */
func TestLoadReadsRateLimits(t *testing.T) {
	t.Setenv("GN_OBJECT_STORE_PROVIDER", "fake")
	t.Setenv("GN_RATE_GLOBAL_PER_MIN", "5000")
	t.Setenv("GN_RATE_WRITE_PER_MIN", "3000")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.RateLimits.GlobalPerMinute != 5000 || cfg.RateLimits.WritePerMinute != 3000 {
		t.Fatalf("限流配置 = %+v，期望 5000/3000", cfg.RateLimits)
	}
	if cfg.RateLimits.LoginPerMinute != 10 || cfg.RateLimits.UploadPerHour != 100 || cfg.RateLimits.PublicSharePerMinute != 60 {
		t.Fatalf("其余限流配置未取默认值: %+v", cfg.RateLimits)
	}
}
