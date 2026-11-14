/*
 * 预签名的纯本地行为：地址主机来自配置端点、强制头携带声明类型。
 * 远端可达性由联调与集成测试覆盖，这里不发起网络请求。
 */
package objectstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

/* TestPresignPutCarriesContentType 校验预签名地址与强制头 */
func TestPresignPutCarriesContentType(t *testing.T) {
	client, err := minio.New("127.0.0.1:9000", &minio.Options{
		Creds:  credentials.NewStaticV4("gleannote", "secret", ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("创建客户端: %v", err)
	}
	store := &MinIO{client: client, bucket: "gleannote-private"}

	target, err := store.PresignPut(context.Background(), "u/abcd/2026/11/x.jpg", "image/jpeg", time.Minute)
	if err != nil {
		t.Fatalf("生成预签名失败: %v", err)
	}
	if target.Headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("headers = %+v，期望包含 Content-Type", target.Headers)
	}
	if !strings.Contains(target.URL, "127.0.0.1:9000") {
		t.Fatalf("putUrl = %q，期望沿用配置端点", target.URL)
	}
	if !strings.Contains(target.URL, "X-Amz-Signature") {
		t.Fatalf("putUrl 缺少签名参数: %q", target.URL)
	}
}
