/* 转换任务队列的 Redis 实现：可靠投递（领取即转入在途表，确认后删除） */
package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gleannote/gleannote/internal/application/port"
)

/* queueSeparator 分隔队列元素中的账号与任务 ID，两者均不含该控制字符 */
const queueSeparator = "\x1f"

/* encodeQueueEntry 把账号与任务 ID 编码为单个队列元素 */
func encodeQueueEntry(userID, jobID string) string {
	return userID + queueSeparator + jobID
}

/* decodeQueueEntry 拆分队列元素；缺少分隔符说明元素来自旧版本，按仅含任务 ID 处理 */
func decodeQueueEntry(entry string) (string, string) {
	userID, jobID, found := strings.Cut(entry, queueSeparator)
	if !found {
		return "", entry
	}
	return userID, jobID
}

/* queueKey 是唯一的转换任务队列键 */
const queueKey = "queue:conversion"

/* processingKey 是在途任务集合键：Worker 领取后暂存，确认处理后删除 */
const processingKey = "queue:conversion:processing"

/* ConversionQueue 是 port.ConversionQueue 的 Redis 实现 */
type ConversionQueue struct {
	client *Client
}

/* NewConversionQueue 绑定共享客户端 */
func NewConversionQueue(client *Client) *ConversionQueue {
	return &ConversionQueue{client: client}
}

/* Enqueue 把「账号 + 任务 ID」追加到队列尾部（FIFO） */
func (q *ConversionQueue) Enqueue(ctx context.Context, userID, jobID string) error {
	if err := q.client.rdb.RPush(ctx, key(queueKey), encodeQueueEntry(userID, jobID)).Err(); err != nil {
		return fmt.Errorf("入队转换任务: %w", err)
	}
	return nil
}

/*
 * Next 阻塞领取下一任务，并把它转入在途集合。
 * 使用 1 秒超时的 BLMOVE 轮询，保证 ctx 取消后能及时返回 ok=false；
 * 领取与登记是原子操作，进程崩溃时任务仍留在在途集合，由 Recover 重新入队。
 */
func (q *ConversionQueue) Next(ctx context.Context) (string, string, bool, error) {
	for {
		if ctx.Err() != nil {
			return "", "", false, nil
		}
		entry, err := q.client.rdb.BLMove(ctx, key(queueKey), key(processingKey),
			"LEFT", "RIGHT", time.Second).Result()
		if err == nil {
			userID, jobID := decodeQueueEntry(entry)
			return userID, jobID, true, nil
		}
		if err == goredis.Nil || ctx.Err() != nil {
			continue
		}
		return "", "", false, fmt.Errorf("领取转换任务: %w", err)
	}
}

/* Ack 从在途集合移除已处理的任务；重复确认不报错 */
func (q *ConversionQueue) Ack(ctx context.Context, userID, jobID string) error {
	if err := q.client.rdb.LRem(ctx, key(processingKey), 1, encodeQueueEntry(userID, jobID)).Err(); err != nil {
		return fmt.Errorf("确认转换任务: %w", err)
	}
	return nil
}

/*
 * Recover 把上次进程退出时留在在途集合的任务重新放回队列。
 * 任务可能被重复投递，状态机 CAS 会保证只有一次真正执行。
 */
func (q *ConversionQueue) Recover(ctx context.Context) error {
	for {
		jobID, err := q.client.rdb.LMove(ctx, key(processingKey), key(queueKey),
			"LEFT", "RIGHT").Result()
		if err == goredis.Nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("恢复在途任务: %w", err)
		}
		if jobID == "" {
			return nil
		}
	}
}

/* 确保实现端口契约 */
var _ port.ConversionQueue = (*ConversionQueue)(nil)
