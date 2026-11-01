/*
 * 进程内转换 Worker：消费内存队列并执行转换任务。
 *
 * GN_MODE=all 时与 HTTP 同进程运行；GN_MODE=worker 时只运行该循环。
 * 参考：docs/ARCHITECTURE.md#55-异步与任务队列、docs/ARCHITECTURE.md#62-图片上传到转换完成
 */
package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	conversionusecase "github.com/gleannote/gleannote/internal/application/usecase/conversion"
	folderusecase "github.com/gleannote/gleannote/internal/application/usecase/folder"
	noteusecase "github.com/gleannote/gleannote/internal/application/usecase/note"
)

const (
	/* conversionQueueBuffer 是队列缓冲深度，避免瞬时突增阻塞 HTTP 请求 */
	conversionQueueBuffer = 64
	/* conversionWorkerCount 是并发消费协程数；识图为 IO 密集，当前阶段 2 个足够 */
	conversionWorkerCount = 2
	/* queueReconcileInterval 是补偿入队间隔，覆盖投递丢失与用户串行等待 */
	queueReconcileInterval = time.Minute
	/* queueStaleAfter 是任务停留在排队状态的补偿阈值，避免正常等待被反复入队 */
	queueStaleAfter = 2 * time.Minute
	/* queueRecoverBatch 是单轮恢复/补偿入队的任务上限 */
	queueRecoverBatch = 200
	/* purgeInterval 是回收站到期清理扫描间隔；1 小时足以满足 30 天保留口径 */
	purgeInterval = time.Hour
	/* purgeBatchLimit 是单轮清理上限，避免一次性长时间占用数据库连接 */
	purgeBatchLimit = 100
)

/* conversionWorker 管理消费协程的生命周期；Close 后不再领取新任务 */
type conversionWorker struct {
	queue   port.ConversionQueue
	service *conversionusecase.Service
	logger  *slog.Logger
	cancel  context.CancelFunc
	group   sync.WaitGroup
}

/* startConversionWorker 启动固定数量的消费协程并立即返回 */
func startConversionWorker(service *conversionusecase.Service, queue port.ConversionQueue,
	logger *slog.Logger) *conversionWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &conversionWorker{
		queue:   queue,
		service: service,
		logger:  logger,
		cancel:  cancel,
	}
	/* 先启动消费协程，再恢复投递：内存队列缓冲有限，无消费者时入队会阻塞 */
	worker.group.Add(conversionWorkerCount + 1)
	for index := 0; index < conversionWorkerCount; index++ {
		go func() {
			defer worker.group.Done()
			worker.loop(ctx)
		}()
	}
	/* 补偿协程定期把长时间停留的任务重新入队，覆盖投递丢失与用户串行等待 */
	go func() {
		defer worker.group.Done()
		worker.reconcile(ctx)
	}()

	/* 启动恢复：先回收上次进程遗留的在途任务，再把持久化的排队任务重新投递 */
	if err := queue.Recover(ctx); err != nil {
		logger.Error("恢复在途转换任务失败", "error", err)
	}
	if count, err := service.RecoverPending(ctx, queueRecoverBatch); err != nil {
		logger.Error("恢复排队转换任务失败", "error", err)
	} else if count > 0 {
		logger.Info("已重新投递排队转换任务", "count", count)
	}
	return worker
}

/*
 * loop 阻塞领取任务并执行。
 * 上下文取消或队列关闭时退出；单任务错误只记录日志，不终止消费循环。
 * 无论执行结果如何都确认任务：失败由用例写入终态，进程停机（上下文取消）时用例把任务回退 QUEUED，
 * 由启动恢复或补偿协程重新投递，避免毒消息阻塞队列，也避免滚动发布丢失在途任务。
 */
func (w *conversionWorker) loop(ctx context.Context) {
	for {
		userID, jobID, ok, err := w.queue.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Error("读取转换队列失败", "error", err)
			continue
		}
		if !ok {
			return
		}
		if err := w.service.RunJob(ctx, userID, jobID); err != nil {
			w.logger.Error("执行转换任务失败", "jobId", jobID, "error", err)
		}
		if err := w.queue.Ack(ctx, userID, jobID); err != nil && ctx.Err() == nil {
			w.logger.Error("确认转换任务失败", "jobId", jobID, "error", err)
		}
	}
}

/* reconcile 周期性补偿入队长时间排队未被执行的任务 */
func (w *conversionWorker) reconcile(ctx context.Context) {
	ticker := time.NewTicker(queueReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := w.service.RequeueStale(ctx, queueStaleAfter, queueRecoverBatch)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Error("补偿入队转换任务失败", "error", err)
				}
				continue
			}
			if count > 0 {
				w.logger.Info("已补偿入队转换任务", "count", count)
			}
		}
	}
}

/*
 * Close 取消上下文并等待全部协程退出。
 * 队列自身的释放（关闭通道或断开 Redis）由装配层登记的释放钩子负责；可重复调用。
 */
func (w *conversionWorker) Close() error {
	w.cancel()
	w.group.Wait()
	return nil
}

/*
 * trashPurger 按固定间隔彻底删除 purge_at 已到期的回收站内容。
 * 先清理目录再清理笔记：目录级联会带走子树笔记，顺序相反会重复扫描。
 * 单实例运行（GN_MODE=all/worker），多实例部署时由数据库行锁与 404 语义保证幂等。
 * 参考：docs/ARCHITECTURE.md#64-回收站恢复与到期清理
 */
type trashPurger struct {
	folders *folderusecase.Service
	notes   *noteusecase.Service
	logger  *slog.Logger
	cancel  context.CancelFunc
	done    chan struct{}
}

/* startTrashPurger 启动清理循环并立即执行一轮，确保重启后马上回收积压数据 */
func startTrashPurger(folders *folderusecase.Service, notes *noteusecase.Service,
	logger *slog.Logger) *trashPurger {
	ctx, cancel := context.WithCancel(context.Background())
	purger := &trashPurger{
		folders: folders,
		notes:   notes,
		logger:  logger,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go purger.loop(ctx)
	return purger
}

/* loop 周期性执行清理，上下文取消时退出 */
func (p *trashPurger) loop(ctx context.Context) {
	defer close(p.done)
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	p.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.runOnce(ctx)
		}
	}
}

/* runOnce 执行一轮清理；失败只记录日志，不终止后台循环 */
func (p *trashPurger) runOnce(ctx context.Context) {
	folderCount, err := p.folders.PurgeExpiredFolders(ctx, purgeBatchLimit)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Error("清理到期目录失败", "error", err)
		}
	} else if folderCount > 0 {
		p.logger.Info("已清理到期目录", "count", folderCount)
	}

	noteCount, err := p.notes.PurgeExpiredNotes(ctx, purgeBatchLimit)
	if err != nil {
		if ctx.Err() == nil {
			p.logger.Error("清理到期笔记失败", "error", err)
		}
		return
	}
	if noteCount > 0 {
		p.logger.Info("已清理到期笔记", "count", noteCount)
	}
}

/* Close 取消上下文并等待循环退出；可重复调用 */
func (p *trashPurger) Close() error {
	p.cancel()
	<-p.done
	return nil
}
