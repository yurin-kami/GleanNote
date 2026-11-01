/*
 * Package main 启动拾页笔记服务端单二进制。
 *
 * 启动顺序：加载配置 → 装配适配器 → 监听 HTTP → 等待退出信号 → 优雅关闭。
 * 参考：docs/ARCHITECTURE.md#74-环境与配置、docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略
 */
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gleannote/gleannote/internal/bootstrap"
	"github.com/gleannote/gleannote/internal/config"
)

/* shutdownTimeout 是优雅退出的最长等待时间，超时后强制断开连接 */
const shutdownTimeout = 10 * time.Second

/* main 只负责进程退出码，业务错误统一由 run 返回 */
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

/*
 * run 完成配置加载、装配与监听，并在收到中断信号后优雅关闭。
 * 任何装配错误都必须终止进程，不允许带病提供服务。
 */
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)

	app, err := bootstrap.New(cfg, os.Stdout, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			logger.Error("释放资源失败", "error", closeErr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	/*
	 * GN_MODE=worker 不监听 HTTP：进程只承载转换 Worker，
	 * 等待退出信号后由 app.Close 停止消费并等待在途任务收尾。
	 */
	if cfg.Mode == "worker" {
		logger.Info("Worker 模式已启动，未监听 HTTP")
		<-ctx.Done()
		logger.Info("收到退出信号，开始优雅关闭")
		return nil
	}
	return serve(ctx, cfg.HTTPAddr, app.Router, logger)
}

/*
 * serve 启动 HTTP 服务并阻塞，直到监听失败或收到退出信号。
 * 退出时先停止接收新请求，再在超时时间内等待在途请求完成。
 */
func serve(ctx context.Context, addr string, handler http.Handler, logger *slog.Logger) error {
	/* 显式限制读写时间与头部大小，避免慢速请求或超大头部拖垮进程 */
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	/* 监听错误通过通道回传，避免在 select 中丢失启动失败原因 */
	failed := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()
	logger.Info("HTTP 服务已启动", "addr", addr)

	select {
	case err := <-failed:
		return fmt.Errorf("监听 %s: %w", addr, err)
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭: %w", err)
	}
	logger.Info("服务已关闭")
	return nil
}
