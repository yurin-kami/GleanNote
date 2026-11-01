/*
 * Package objectstore 提供对象存储能力的适配器与工厂。
 *
 * 生产与本地联调走 MinIO/S3 适配器（minio.go），Fake 仅用于单元测试与离线演示。
 * 参考：docs/ARCHITECTURE.md#56-页面图片管线、docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package objectstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/platform/clock"
)

/*
 * Fake 是开发态对象存储：对象元数据保存在内存，预签名地址使用 memory:// 协议。
 * 进程重启即清空，禁止用于任何真实数据或容量评估。
 */
type Fake struct {
	mu      sync.Mutex
	clock   clock.Clock
	objects map[string]fakeObject
}

/* fakeObject 同时保留元数据与字节，Stat 与 Get 读取同一份记录 */
type fakeObject struct {
	info port.ObjectInfo
	data []byte
}

/* NewFake 创建空的内存对象存储；注入时钟使预签名过期时间可确定 */
func NewFake(now clock.Clock) *Fake {
	return &Fake{clock: now, objects: make(map[string]fakeObject)}
}

/*
 * New 按名称创建对象存储提供者。
 * minio/s3 走真实适配器；fake 供测试并复用传入时钟；其余名称必须失败，禁止静默降级。
 */
func New(ctx context.Context, name string, opts Options, now clock.Clock) (port.ObjectStore, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "fake":
		return NewFake(now), nil
	case "minio", "s3":
		return NewMinIO(ctx, opts)
	default:
		return nil, fmt.Errorf("%w: object store provider %q", port.ErrProviderNotConfigured, name)
	}
}

/* Name 返回提供者名称，用于启动日志与就绪探针展示 */
func (f *Fake) Name() string { return "fake" }

/*
 * PresignPut 返回 memory:// 假地址，客户端无法真正上传。
 * 联调时改用 Put 直接写入内存，语义等价于「上传成功」。
 */
func (f *Fake) PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (port.PutTarget, error) {
	if err := ctx.Err(); err != nil {
		return port.PutTarget{}, err
	}
	if err := checkKey(key); err != nil {
		return port.PutTarget{}, err
	}
	if strings.TrimSpace(contentType) == "" {
		return port.PutTarget{}, fmt.Errorf("contentType 不能为空")
	}
	expiresAt := f.clock.Now().Add(ttl)
	return port.PutTarget{
		URL:       f.sign(key, "put", expiresAt),
		Headers:   map[string]string{"Content-Type": contentType},
		ExpiresAt: expiresAt,
	}, nil
}

/* PresignGet 与真实 S3 一致，不校验对象是否存在，仅生成带过期的地址 */
func (f *Fake) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	if err := checkKey(key); err != nil {
		return "", time.Time{}, err
	}
	expiresAt := f.clock.Now().Add(ttl)
	return f.sign(key, "get", expiresAt), expiresAt, nil
}

/* sign 拼装内存协议地址：memory://fake/<key>?op=&expires= */
func (f *Fake) sign(key, op string, expiresAt time.Time) string {
	target := url.URL{Scheme: "memory", Host: "fake", Path: "/" + key}
	query := target.Query()
	query.Set("op", op)
	query.Set("expires", expiresAt.UTC().Format(time.RFC3339))
	target.RawQuery = query.Encode()
	return target.String()
}

/* Stat 返回对象元数据；不存在时返回 ErrObjectNotFound */
func (f *Fake) Stat(ctx context.Context, key string) (port.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return port.ObjectInfo{}, err
	}
	if err := checkKey(key); err != nil {
		return port.ObjectInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[key]
	if !ok {
		return port.ObjectInfo{}, port.ErrObjectNotFound
	}
	return object.info, nil
}

/* Delete 删除对象；不存在时返回 ErrObjectNotFound 以便用例感知重复删除 */
func (f *Fake) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objects[key]; !ok {
		return port.ErrObjectNotFound
	}
	delete(f.objects, key)
	return nil
}

/*
 * Put 由服务端写入对象字节。
 * 测试与联调也用它模拟「客户端已完成预签名上传」的结果。
 */
func (f *Fake) Put(ctx context.Context, key, contentType string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(contentType) == "" {
		return fmt.Errorf("contentType 不能为空")
	}
	copied := append([]byte(nil), data...)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{
		info: port.ObjectInfo{Key: key, Size: int64(len(copied)), ContentType: contentType},
		data: copied,
	}
	return nil
}

/* Get 返回对象字节副本；不存在时返回 ErrObjectNotFound */
func (f *Fake) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkKey(key); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[key]
	if !ok {
		return nil, port.ErrObjectNotFound
	}
	return append([]byte(nil), object.data...), nil
}

/* Health 无外部依赖，仅在 context 已取消时返回错误 */
func (f *Fake) Health(ctx context.Context) error {
	return ctx.Err()
}

/*
 * checkKey 拒绝空值、绝对路径与上跳片段，避免键被解释为越权路径。
 * 键的生成规则见 docs/ARCHITECTURE.md#56-页面图片管线。
 */
func checkKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("object key 不能为空")
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("object key 不能以 / 开头: %q", key)
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("object key 不能包含 ..: %q", key)
	}
	if strings.Contains(key, "://") {
		return fmt.Errorf("object key 不能包含协议前缀: %q", key)
	}
	return nil
}
