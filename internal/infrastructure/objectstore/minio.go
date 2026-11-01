/*
 * MinIO / S3 兼容对象存储适配器。
 *
 * 本地开发由容器化 MinIO 提供；因为走 S3 协议，生产可迁移到同类服务而无需改动业务代码。
 * 参考：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* 预签名默认有效期与上限，上限与 MinIO 服务端限制保持一致 */
const (
	defaultPresignTTL = 15 * time.Minute
	maxPresignTTL     = 7 * 24 * time.Hour
)

/* Options 是适配器连接参数，来源为环境变量，清单见 docs/ARCHITECTURE.md 附录 A */
type Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

/* MinIO 是 port.ObjectStore 的 S3 兼容实现，桶保持私有 */
type MinIO struct {
	client *minio.Client
	bucket string
}

/*
 * NewMinIO 建立客户端并确保桶存在。
 * 只创建桶本身，不设置任何公开读策略；访问一律通过短期预签名地址。
 */
func NewMinIO(ctx context.Context, opts Options) (*MinIO, error) {
	opts, err := opts.normalized()
	if err != nil {
		return nil, err
	}
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.UseSSL,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 MinIO 客户端: %w", err)
	}
	if err := ensureBucket(ctx, client, opts); err != nil {
		return nil, err
	}
	return &MinIO{client: client, bucket: opts.Bucket}, nil
}

/*
 * normalized 规范化端点并补齐默认区域。
 * 非法配置必须在装配期暴露，避免运行期才发现桶名或凭据错误。
 */
func (o Options) normalized() (Options, error) {
	endpoint, secure, err := parseEndpoint(o.Endpoint, o.UseSSL)
	if err != nil {
		return Options{}, err
	}
	o.Endpoint = endpoint
	o.UseSSL = secure
	o.Bucket = strings.TrimSpace(o.Bucket)
	o.Region = strings.TrimSpace(o.Region)
	if o.Region == "" {
		o.Region = "us-east-1"
	}
	if o.Bucket == "" {
		return Options{}, fmt.Errorf("对象存储桶名不能为空")
	}
	if strings.TrimSpace(o.AccessKey) == "" || strings.TrimSpace(o.SecretKey) == "" {
		return Options{}, fmt.Errorf("对象存储访问密钥不能为空")
	}
	return o, nil
}

/*
 * parseEndpoint 接受 host:port 或带 http/https 的 URL。
 * 显式协议优先于 GN_S3_USE_SSL，避免「写了 https 却按明文连接」的歧义。
 */
func parseEndpoint(raw string, useSSL bool) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false, fmt.Errorf("对象存储端点不能为空")
	}
	if !strings.Contains(value, "://") {
		return strings.TrimSuffix(value, "/"), useSSL, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false, fmt.Errorf("解析对象存储端点: %w", err)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false, fmt.Errorf("对象存储端点不支持路径: %q", raw)
	}
	switch parsed.Scheme {
	case "https":
		return parsed.Host, true, nil
	case "http":
		return parsed.Host, false, nil
	default:
		return "", false, fmt.Errorf("对象存储端点协议非法: %q", parsed.Scheme)
	}
}

/*
 * ensureBucket 在启动时确认桶可用，缺失时自动创建。
 * 并发启动可能同时创建，已存在视为成功。
 */
func ensureBucket(ctx context.Context, client *minio.Client, opts Options) error {
	exists, err := client.BucketExists(ctx, opts.Bucket)
	if err != nil {
		return fmt.Errorf("检查桶 %s: %w", opts.Bucket, err)
	}
	if exists {
		return nil
	}
	err = client.MakeBucket(ctx, opts.Bucket, minio.MakeBucketOptions{Region: opts.Region})
	if err == nil {
		return nil
	}
	switch minio.ToErrorResponse(err).Code {
	case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
		return nil
	default:
		return fmt.Errorf("创建桶 %s: %w", opts.Bucket, err)
	}
}

/* Name 返回提供者名称，用于启动日志与就绪探针展示 */
func (m *MinIO) Name() string { return "minio" }

/*
 * PresignPut 生成短期上传地址。
 * MinIO 的预签名 PUT 不绑定 Content-Type，因此 Headers 为空；客户端可自行设置。
 */
func (m *MinIO) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (port.PutTarget, error) {
	if err := ctx.Err(); err != nil {
		return port.PutTarget{}, err
	}
	if err := checkKey(key); err != nil {
		return port.PutTarget{}, err
	}
	if strings.TrimSpace(contentType) == "" {
		return port.PutTarget{}, fmt.Errorf("contentType 不能为空")
	}
	expires := effectiveTTL(ttl)
	target, err := m.client.PresignedPutObject(ctx, m.bucket, key, expires)
	if err != nil {
		return port.PutTarget{}, fmt.Errorf("生成上传地址: %w", err)
	}
	return port.PutTarget{
		URL:       target.String(),
		Headers:   map[string]string{},
		ExpiresAt: time.Now().Add(expires),
	}, nil
}

/* PresignGet 生成短期下载地址；与 S3 语义一致，不校验对象是否存在 */
func (m *MinIO) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if err := checkKey(key); err != nil {
		return "", time.Time{}, err
	}
	expires := effectiveTTL(ttl)
	target, err := m.client.PresignedGetObject(ctx, m.bucket, key, expires, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("生成下载地址: %w", err)
	}
	return target.String(), time.Now().Add(expires), nil
}

/* Stat 返回对象元数据；对象不存在映射为 port.ErrObjectNotFound */
func (m *MinIO) Stat(ctx context.Context, key string) (port.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return port.ObjectInfo{}, err
	}
	if err := checkKey(key); err != nil {
		return port.ObjectInfo{}, err
	}
	info, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return port.ObjectInfo{}, port.ErrObjectNotFound
		}
		return port.ObjectInfo{}, fmt.Errorf("读取对象元数据 %s: %w", key, err)
	}
	return port.ObjectInfo{Key: info.Key, Size: info.Size, ContentType: info.ContentType}, nil
}

/*
 * Put 由服务端写入对象字节，用于缩略图等派生文件。
 * 长度与内容类型显式传入，避免 MinIO 自动嗅探导致类型漂移。
 */
func (m *MinIO) Put(ctx context.Context, key, contentType string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(contentType) == "" {
		return fmt.Errorf("contentType 不能为空")
	}
	_, err := m.client.PutObject(ctx, m.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("写入对象 %s: %w", key, err)
	}
	return nil
}

/* Get 读取对象完整字节；对象不存在映射为 port.ErrObjectNotFound */
func (m *MinIO) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	object, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, port.ErrObjectNotFound
		}
		return nil, fmt.Errorf("读取对象 %s: %w", key, err)
	}
	defer object.Close()

	data, err := io.ReadAll(object)
	if err != nil {
		if isNotFound(err) {
			return nil, port.ErrObjectNotFound
		}
		return nil, fmt.Errorf("读取对象 %s: %w", key, err)
	}
	return data, nil
}

/*
 * Delete 删除对象。
 * S3 语义下重复删除同样返回成功，因此这里不区分对象是否存在。
 */
func (m *MinIO) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if err := m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("删除对象 %s: %w", key, err)
	}
	return nil
}

/*
 * Health 检查桶可达性，供就绪探针使用。
 * 错误文本只保留桶名，不暴露内网地址与凭据。
 */
func (m *MinIO) Health(ctx context.Context) error {
	exists, err := m.client.BucketExists(ctx, m.bucket)
	if err != nil {
		return fmt.Errorf("%w: minio 桶 %s 不可访问", port.ErrProviderUnavailable, m.bucket)
	}
	if !exists {
		return fmt.Errorf("%w: minio 桶 %s 不存在", port.ErrProviderUnavailable, m.bucket)
	}
	return nil
}

/* isNotFound 识别 S3 的「对象不存在」错误码 */
func isNotFound(err error) bool {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NotFound":
		return true
	default:
		return false
	}
}

/* effectiveTTL 收敛预签名有效期：缺省 15 分钟，上限 7 天 */
func effectiveTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return defaultPresignTTL
	}
	if ttl > maxPresignTTL {
		return maxPresignTTL
	}
	return ttl
}
