/*
 * MinIO 适配器的纯函数测试：端点规范化、必填校验与预签名有效期收敛。
 * 网络相关行为依赖本地容器，不在单元测试中覆盖。
 */
package objectstore

import (
	"strings"
	"testing"
	"time"
)

/* TestParseEndpoint 校验 host:port 与带协议 URL 的解析口径 */
func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		raw    string
		useSSL bool
		want   string
		secure bool
	}{
		{"127.0.0.1:9000", false, "127.0.0.1:9000", false},
		{"127.0.0.1:9000/", false, "127.0.0.1:9000", false},
		{"minio.local:9000", true, "minio.local:9000", true},
		{"http://minio.local:9000", true, "minio.local:9000", false},
		{"https://minio.local:9000", false, "minio.local:9000", true},
	}
	for _, item := range cases {
		endpoint, secure, err := parseEndpoint(item.raw, item.useSSL)
		if err != nil {
			t.Fatalf("%q 解析失败: %v", item.raw, err)
		}
		if endpoint != item.want || secure != item.secure {
			t.Fatalf("%q => (%q,%v)，期望 (%q,%v)", item.raw, endpoint, secure, item.want, item.secure)
		}
	}
}

/* TestParseEndpointRejects 校验非法协议与带路径端点被拒绝 */
func TestParseEndpointRejects(t *testing.T) {
	for _, raw := range []string{"", "ftp://minio.local", "https://minio.local/api"} {
		if _, _, err := parseEndpoint(raw, false); err == nil {
			t.Fatalf("%q 应返回错误", raw)
		}
	}
}

/* TestOptionsNormalized 校验缺省区域与必填项 */
func TestOptionsNormalized(t *testing.T) {
	opts, err := Options{
		Endpoint:  "127.0.0.1:9000",
		Bucket:    "gleannote-private",
		AccessKey: "gleannote",
		SecretKey: "secret",
	}.normalized()
	if err != nil {
		t.Fatalf("规范化失败: %v", err)
	}
	if opts.Region != "us-east-1" {
		t.Fatalf("Region = %q，期望 us-east-1", opts.Region)
	}

	_, err = Options{Endpoint: "127.0.0.1:9000", Bucket: "b"}.normalized()
	if err == nil || !strings.Contains(err.Error(), "访问密钥") {
		t.Fatalf("缺少密钥应报错，实际 %v", err)
	}
}

/* TestEffectiveTTL 校验默认值与上限收敛 */
func TestEffectiveTTL(t *testing.T) {
	if got := effectiveTTL(0); got != defaultPresignTTL {
		t.Fatalf("零值 => %v，期望 %v", got, defaultPresignTTL)
	}
	if got := effectiveTTL(200 * time.Hour); got != maxPresignTTL {
		t.Fatalf("超上限 => %v，期望 %v", got, maxPresignTTL)
	}
	if got := effectiveTTL(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("合法值 => %v，期望 5m", got)
	}
}
