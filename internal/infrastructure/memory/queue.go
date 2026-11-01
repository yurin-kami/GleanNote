/* 内存转换任务队列：以通道模拟异步入队与阻塞消费，供进程内 Worker 使用 */
package memory

import (
	"context"
	"errors"
	"sync"
)

/* ErrQueueClosed 表示队列已关闭，入队不再被接受 */
var ErrQueueClosed = errors.New("conversion queue closed")

/* queueItem 同时携带任务归属账号，保证 Worker 可按 userID 限定取数 */
type queueItem struct {
	userID string
	jobID  string
}

/* ConversionQueue 是 port.ConversionQueue 的内存实现 */
type ConversionQueue struct {
	queue chan queueItem
	done  chan struct{}
	once  sync.Once
}

/* NewConversionQueue 创建带缓冲的队列；buffer 为 0 时等价于无缓冲通道 */
func NewConversionQueue(buffer int) *ConversionQueue {
	if buffer < 0 {
		buffer = 0
	}
	return &ConversionQueue{
		queue: make(chan queueItem, buffer),
		done:  make(chan struct{}),
	}
}

/*
 * Enqueue 把任务 ID 放入队列。
 * 队列关闭后返回 ErrQueueClosed；上下文取消时放弃入队，避免阻塞调用方。
 */
func (q *ConversionQueue) Enqueue(ctx context.Context, userID, jobID string) error {
	select {
	case <-q.done:
		return ErrQueueClosed
	default:
	}
	select {
	case <-q.done:
		return ErrQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	case q.queue <- queueItem{userID: userID, jobID: jobID}:
		return nil
	}
}

/*
 * Next 阻塞等待下一个任务。
 * 队列关闭或上下文取消时返回 ok=false；已入队任务在关闭后不再保证被消费。
 */
func (q *ConversionQueue) Next(ctx context.Context) (string, string, bool, error) {
	select {
	case <-ctx.Done():
		return "", "", false, ctx.Err()
	case <-q.done:
		return "", "", false, nil
	case item := <-q.queue:
		return item.userID, item.jobID, true, nil
	}
}

/* Close 关闭队列，通知 Worker 退出；可重复调用 */
func (q *ConversionQueue) Close() {
	q.once.Do(func() { close(q.done) })
}

/*
 * Ack 确认任务已处理。
 * 内存队列在 Next 返回时即完成投递，没有待确认集合，因此确认是空操作。
 */
func (q *ConversionQueue) Ack(ctx context.Context, userID, jobID string) error { return nil }

/*
 * Recover 恢复未确认的任务。
 * 内存队列任务只存在于进程内通道，进程重启后由持久化的 QUEUED 任务重新入队，因此无需额外恢复。
 */
func (q *ConversionQueue) Recover(ctx context.Context) error { return nil }
