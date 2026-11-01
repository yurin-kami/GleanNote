/*
 * 装配测试：校验生产环境禁止 Fake、开发环境可装配且提供者名称正确。
 * 规则来源：docs/ARCHITECTURE.md#816-付费第三方服务的端口与-fake-策略。
 */
package bootstrap_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/bootstrap"
	"github.com/gleannote/gleannote/internal/config"
)

/* quietLogger 丢弃日志输出，避免测试噪音 */
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

/* TestNewRejectsFakeInProd 校验生产环境出现 Fake 时启动失败 */
func TestNewRejectsFakeInProd(t *testing.T) {
	cfg := config.Config{Env: "prod", Mode: "all"}
	app, err := bootstrap.New(cfg, io.Discard, quietLogger())
	if app != nil {
		t.Fatalf("生产环境不应装配成功")
	}
	if !errors.Is(err, port.ErrProviderNotConfigured) {
		t.Fatalf("返回 %v，期望 ErrProviderNotConfigured", err)
	}
}

/* TestNewBuildsDevApp 校验开发环境装配成功且三项能力均为 fake */
func TestNewBuildsDevApp(t *testing.T) {
	cfg := config.Config{
		Env:           "dev",
		Mode:          "all",
		PublicBaseURL: "https://gleannote.example",
		Auth:          config.AuthConfig{HMACSecret: strings.Repeat("k", 32)},
	}
	app, err := bootstrap.New(cfg, io.Discard, quietLogger())
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	defer func() {
		if closeErr := app.Close(); closeErr != nil {
			t.Fatalf("关闭失败: %v", closeErr)
		}
	}()

	if app.Router == nil {
		t.Fatalf("路由为空")
	}
	if app.Providers.Vision.Name() != "fake" {
		t.Fatalf("vision = %q，期望 fake", app.Providers.Vision.Name())
	}
	if app.Providers.Mailer.Name() != "fake" {
		t.Fatalf("mailer = %q，期望 fake", app.Providers.Mailer.Name())
	}
	if app.Providers.ObjectStore.Name() != "fake" {
		t.Fatalf("objectStore = %q，期望 fake", app.Providers.ObjectStore.Name())
	}
}
