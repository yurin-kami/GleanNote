/* CORS 白名单解析与默认关闭：默认不放行任何跨源请求，配置后按去重顺序生效 */
package config

import "testing"

/* TestParseOriginsTrimsAndDedupes 校验空白项被丢弃、重复项合并且保留声明顺序 */
func TestParseOriginsTrimsAndDedupes(t *testing.T) {
	origins := parseOrigins(" http://a.example , http://b.example ,http://a.example, ,")
	if len(origins) != 2 || origins[0] != "http://a.example" || origins[1] != "http://b.example" {
		t.Fatalf("解析结果 = %v，期望 [http://a.example http://b.example]", origins)
	}
	if got := parseOrigins("   "); len(got) != 0 {
		t.Fatalf("空白配置 = %v，期望空列表", got)
	}
}

/* TestCORSDisabledByDefault 校验未配置时各环境都不开启跨域 */
func TestCORSDisabledByDefault(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		cfg := Config{Env: env}
		if len(cfg.CORSOrigins) != 0 {
			t.Fatalf("%s 环境默认 CORS 白名单 = %v，期望为空", env, cfg.CORSOrigins)
		}
	}
}
