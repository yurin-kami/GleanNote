/*
 * 对象存储端口。
 *
 * 当前开发态使用内存 Fake；切换阿里云 OSS / S3 后按量计费，
 * 因此业务代码只持有 object key，不感知供应商。
 * 参考：docs/ARCHITECTURE.md#56-页面图片管线、docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */

package port

import (
	"context"
	"errors"
	"time"
)

/*
 * PutTarget 是预签名上传目标。
 * Headers 是提供者要求客户端原样携带的请求头；为空表示无强制头。
 * 改动强制头会导致签名校验失败。
 */
type PutTarget struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

/*
 * ObjectInfo 是对象元数据。
 * Size 用于容量计量与上传完整性校验。
 */
type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
}

/*
 * ObjectStore 是对象存储能力的端口。
 * 所有方法都必须接收 context，且不得在数据库事务内调用。
 */
type ObjectStore interface {
	Name() string
	PresignPut(ctx context.Context, key, contentType string, ttl time.Duration) (PutTarget, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	/* Put 由服务端写入对象字节，用于生成缩略图等派生文件，禁止在数据库事务内调用 */
	Put(ctx context.Context, key, contentType string, data []byte) error
	/* Get 读取对象完整字节，供上传完成校验与图像处理使用，禁止在数据库事务内调用 */
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Health(ctx context.Context) error
}

/* ErrObjectNotFound 表示对象不存在，供用例映射为 404 或补偿任务 */
var ErrObjectNotFound = errors.New("object not found")
