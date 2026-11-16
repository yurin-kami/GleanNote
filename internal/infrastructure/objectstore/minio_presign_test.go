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

/*
 * TestPresignUsesPublicEndpoint 校验配置公网端点后，签名地址只出现在公网主机上。
 * 场景：服务端走内网 minio:9000 读写，客户端必须拿到公网地址才能直传。
 */
func TestPresignUsesPublicEndpoint(t *testing.T) {
	creds := credentials.NewStaticV4("gleannote", "secret", "")
	internal, err := minio.New("minio:9000", &minio.Options{Creds: creds, Secure: false, Region: "us-east-1"})
	if err != nil {
		t.Fatalf("创建内网客户端: %v", err)
	}
	public, err := minio.New("20.214.153.6:9000", &minio.Options{Creds: creds, Secure: false, Region: "us-east-1"})
	if err != nil {
		t.Fatalf("创建公网客户端: %v", err)
	}
	store := &MinIO{client: internal, public: public, bucket: "gleannote-private"}

	put, err := store.PresignPut(context.Background(), "u/abcd/2026/11/x.jpg", "image/jpeg", time.Minute)
	if err != nil {
		t.Fatalf("生成上传地址失败: %v", err)
	}
	if !strings.Contains(put.URL, "20.214.153.6:9000") || strings.Contains(put.URL, "minio:9000") {
		t.Fatalf("putUrl = %q，期望只含公网主机", put.URL)
	}

	url, _, err := store.PresignGet(context.Background(), "thumb/abcd.jpg", time.Minute)
	if err != nil {
		t.Fatalf("生成下载地址失败: %v", err)
	}
	if !strings.Contains(url, "20.214.153.6:9000") || strings.Contains(url, "minio:9000") {
		t.Fatalf("getUrl = %q，期望只含公网主机", url)
	}
}
