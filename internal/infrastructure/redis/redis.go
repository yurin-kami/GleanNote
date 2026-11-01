/*
 * Package redis 提供限流、失败守卫、幂等、令牌黑名单与转换队列的 Redis 实现。
 *
 * 职责：把多实例共享的安全与异步状态收敛到一处，替代进程内实现。
 * 不负责：业务实体持久化；业务数据仍由 infrastructure/postgres 承担。
 * 依赖：go-redis/v9、application/port、domain/errs。
 * 参考：docs/ARCHITECTURE.md#87-限流与防滥用、docs/ARCHITECTURE.md#88-额度与容量
 */
package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

/* Options 是连接参数，来源为 GN_REDIS_URL */
type Options struct {
	URL string
}

/*
 * Client 包装 go-redis 客户端，供各适配器共享同一连接池。
 * 所有键统一加 gn: 前缀，避免与同实例的其他应用冲突。
 */
type Client struct {
	rdb *goredis.Client
}

/* New 解析连接串并验证连通性；失败必须在装配期暴露 */
func New(ctx context.Context, opts Options) (*Client, error) {
	parsed, err := goredis.ParseURL(strings.TrimSpace(opts.URL))
	if err != nil {
		return nil, fmt.Errorf("解析 GN_REDIS_URL: %w", err)
	}
	parsed.PoolSize = 16
	client := goredis.NewClient(parsed)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接 Redis: %w", err)
	}
	return &Client{rdb: client}, nil
}

/* Close 释放连接池，由装配层登记为释放钩子 */
func (c *Client) Close() error { return c.rdb.Close() }

/* Ping 检查可达性，供就绪探针使用 */
func (c *Client) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

/* Name 返回提供者名称，用于启动日志 */
func (c *Client) Name() string { return "redis" }

/* key 给业务键加统一前缀 */
func key(parts ...string) string { return "gn:" + strings.Join(parts, ":") }

/* nowFunc 便于测试替换时钟；生产固定使用系统时间 */
var nowFunc = time.Now
