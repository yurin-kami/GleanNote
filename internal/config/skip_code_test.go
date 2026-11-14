/* 验证码跳过规则只在 dev 生效，staging/prod 必须保持强制校验 */
package config

import "testing"

/* TestSkipEmailCodeVerificationOnlyInDev 校验环境与跳过开关的绑定关系 */
func TestSkipEmailCodeVerificationOnlyInDev(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"dev", true},
		{"staging", false},
		{"prod", false},
	}
	for _, item := range cases {
		cfg := Config{Env: item.env}
		if got := cfg.SkipEmailCodeVerification(); got != item.want {
			t.Fatalf("GN_ENV=%s 时跳过验证码 = %v，期望 %v", item.env, got, item.want)
		}
	}
}
