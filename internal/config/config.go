/*
 * Package config 从环境变量加载服务运行配置，并提供开发态默认值。
 *
 * 约定：密钥与连接串只允许来自环境变量，仓库内只保留 .env.example 占位值。
 * 参考：docs/ARCHITECTURE.md#74-环境与配置、docs/ARCHITECTURE.md#附录-a配置项清单环境变量
 */
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

/* devHMACSecret 仅用于开发态默认值；staging/prod 必须显式配置 */
const devHMACSecret = "dev-only-hmac-secret-replace-me-32bytes"

/*
 * Config 是进程启动后只读的运行配置。
 * 任何字段在启动后都不得被修改，避免并发读写与隐式状态。
 */
type Config struct {
	Env           string
	Mode          string
	HTTPAddr      string
	LogLevel      string
	PublicBaseURL string
	Providers     Providers
	S3            S3Config
	Vision        VisionConfig
	Auth          AuthConfig
	Database      DatabaseConfig
	Redis         RedisConfig
}

/*
 * VisionConfig 是视觉识别供应商的凭据与端点参数。
 * 只有选择 deepseek（OpenAI 兼容多模态）时才需要 API Key；
 * BaseURL/Model 留空由适配器使用内置默认值，便于切换其他兼容服务。
 */
type VisionConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

/*
 * DatabaseConfig 是业务持久化的连接参数。
 * Repository 允许 memory（单元测试与离线开发）与 postgres（运行态）；
 * 选择 postgres 时 URL 必填，容器编排下由 GN_DATABASE_URL 注入。
 */
type DatabaseConfig struct {
	Repository string
	URL        string
	MaxConns   int32
}

/*
 * RedisConfig 是缓存与异步能力的连接参数。
 * Provider 允许 memory（进程内实现）与 redis（多实例共享限流、幂等与队列）。
 */
type RedisConfig struct {
	Provider string
	URL      string
}

/* AuthConfig 是认证所需的服务端密钥；只允许来自环境变量，仓库内只有占位值 */
type AuthConfig struct {
	HMACSecret  string
	SigningSeed []byte
}

/*
 * S3Config 是对象存储适配器的连接参数。
 * 本地开发对应 MinIO 容器；生产可零改动切换到同语义的 S3 兼容存储。
 */
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

/*
 * Providers 声明外部与付费能力的提供者名称。
 *
 * 视觉识别允许 fake 与 deepseek（OpenAI 兼容多模态）；邮件与对象存储仍只登记 fake/minio，
 * 未实现的名称必须在装配期显式失败，禁止静默降级。
 */
type Providers struct {
	Vision         string
	Mailer         string
	ObjectStore    string
	VisionLatency  time.Duration
	VisionFailure  string
	MailRevealCode bool
}

/*
 * Load 读取环境变量并完成合法性校验。
 * 返回错误时调用方必须终止启动，不允许带病运行。
 */
func Load() (Config, error) {
	cfg := Config{
		Env:           getEnv("GN_ENV", "dev"),
		Mode:          getEnv("GN_MODE", "all"),
		HTTPAddr:      getEnv("GN_HTTP_ADDR", "0.0.0.0:8080"),
		LogLevel:      getEnv("GN_LOG_LEVEL", "info"),
		PublicBaseURL: getEnv("GN_PUBLIC_BASE_URL", "https://gleannote.example"),
		Providers: Providers{
			Vision:        getEnv("GN_VISION_PROVIDER", "fake"),
			Mailer:        getEnv("GN_MAILER_PROVIDER", "fake"),
			ObjectStore:   getEnv("GN_OBJECT_STORE_PROVIDER", "minio"),
			VisionFailure: getEnv("GN_FAKE_VISION_FAILURE", ""),
		},
		S3: S3Config{
			Endpoint:  getEnv("GN_S3_ENDPOINT", "127.0.0.1:9000"),
			Bucket:    getEnv("GN_S3_BUCKET", "gleannote-private"),
			AccessKey: getEnv("GN_S3_ACCESS_KEY", ""),
			SecretKey: getEnv("GN_S3_SECRET_KEY", ""),
			Region:    getEnv("GN_S3_REGION", "us-east-1"),
		},
		Vision: VisionConfig{
			APIKey:  getEnv("GN_VISION_API_KEY", ""),
			BaseURL: getEnv("GN_VISION_BASE_URL", ""),
			Model:   getEnv("GN_VISION_MODEL", ""),
		},
		Auth: AuthConfig{
			HMACSecret: getEnv("GN_HMAC_SECRET", devHMACSecret),
		},
		Database: DatabaseConfig{
			Repository: getEnv("GN_REPOSITORY_PROVIDER", "memory"),
			URL:        getEnv("GN_DATABASE_URL", ""),
		},
		Redis: RedisConfig{
			Provider: getEnv("GN_CACHE_PROVIDER", "memory"),
			URL:      getEnv("GN_REDIS_URL", ""),
		},
	}

	/* 签名种子使用 base64 编码的 32 字节 Ed25519 种子；空值仅 dev 允许 */
	seed, err := decodeSigningSeed(getEnv("GN_JWT_PRIVATE_KEY", ""))
	if err != nil {
		return Config{}, err
	}
	cfg.Auth.SigningSeed = seed

	/*
	 * 时长与布尔类配置单独解析：非法值必须报错，避免被静默忽略。
	 * 参考：docs/CONVENTIONS.md#31-通用 G-05
	 */
	latency, err := time.ParseDuration(getEnv("GN_FAKE_VISION_LATENCY", "0s"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 GN_FAKE_VISION_LATENCY: %w", err)
	}
	cfg.Providers.VisionLatency = latency

	/* 单次模型调用超时；整篇转换仍受用例层 5 分钟上限约束 */
	visionTimeout, err := time.ParseDuration(getEnv("GN_VISION_REQUEST_TIMEOUT", "2m"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 GN_VISION_REQUEST_TIMEOUT: %w", err)
	}
	cfg.Vision.Timeout = visionTimeout

	useSSL, err := strconv.ParseBool(getEnv("GN_S3_USE_SSL", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 GN_S3_USE_SSL: %w", err)
	}
	cfg.S3.UseSSL = useSSL

	/* 开发态邮件默认脱敏输出；仅本地手工联调时显式开启明文验证码 */
	revealCode, err := strconv.ParseBool(getEnv("GN_FAKE_MAIL_REVEAL_CODE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("解析 GN_FAKE_MAIL_REVEAL_CODE: %w", err)
	}
	cfg.Providers.MailRevealCode = revealCode

	/* 连接池上限缺省等于 CPU 核数，避免容器内句柄过多 */
	maxConns, err := strconv.ParseInt(getEnv("GN_DATABASE_MAX_CONNS", "8"), 10, 32)
	if err != nil || maxConns <= 0 {
		return Config{}, fmt.Errorf("解析 GN_DATABASE_MAX_CONNS: %q 必须为正整数", getEnv("GN_DATABASE_MAX_CONNS", ""))
	}
	cfg.Database.MaxConns = int32(maxConns)

	/* 校验枚举与必填项，校验失败直接返回，不修改默认值兜底 */
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

/*
 * validate 检查环境、运行模式与监听地址的合法性。
 * 口径与 docs/ARCHITECTURE.md#附录-a配置项清单环境变量 保持一致。
 */
func (c Config) validate() error {
	switch c.Env {
	case "dev", "staging", "prod":
	default:
		return fmt.Errorf("GN_ENV 非法: %q，允许 dev/staging/prod", c.Env)
	}
	switch c.Mode {
	case "all", "api", "worker":
	default:
		return fmt.Errorf("GN_MODE 非法: %q，允许 all/api/worker", c.Mode)
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("GN_HTTP_ADDR 不能为空")
	}
	if strings.TrimSpace(c.PublicBaseURL) == "" {
		return fmt.Errorf("GN_PUBLIC_BASE_URL 不能为空")
	}
	if err := c.validateAuth(); err != nil {
		return err
	}
	if err := c.validateVision(); err != nil {
		return err
	}
	if err := c.validateStorage(); err != nil {
		return err
	}
	return c.validateS3()
}

/*
 * validateVision 校验视觉提供者名称与必填凭据。
 * deepseek 走 OpenAI 兼容接口，缺少 API Key 时启动即失败，避免运行期才发现计费能力不可用。
 */
func (c Config) validateVision() error {
	switch strings.TrimSpace(strings.ToLower(c.Providers.Vision)) {
	case "", "fake":
		return nil
	case "deepseek":
		if strings.TrimSpace(c.Vision.APIKey) == "" {
			return fmt.Errorf("GN_VISION_API_KEY 在视觉使用 deepseek 时不能为空")
		}
		return nil
	default:
		return fmt.Errorf("GN_VISION_PROVIDER 非法: %q，允许 fake/deepseek", c.Providers.Vision)
	}
}

/*
 * validateStorage 校验持久化与缓存的提供者枚举。
 * 选中 postgres/redis 时必须显式提供连接串，防止运行期才发现未注入环境变量。
 */
func (c Config) validateStorage() error {
	switch strings.TrimSpace(strings.ToLower(c.Database.Repository)) {
	case "memory":
	case "postgres":
		if strings.TrimSpace(c.Database.URL) == "" {
			return fmt.Errorf("GN_DATABASE_URL 在持久化使用 postgres 时不能为空")
		}
	default:
		return fmt.Errorf("GN_REPOSITORY_PROVIDER 非法: %q，允许 memory/postgres", c.Database.Repository)
	}
	switch strings.TrimSpace(strings.ToLower(c.Redis.Provider)) {
	case "memory":
	case "redis":
		if strings.TrimSpace(c.Redis.URL) == "" {
			return fmt.Errorf("GN_REDIS_URL 在缓存使用 redis 时不能为空")
		}
	default:
		return fmt.Errorf("GN_CACHE_PROVIDER 非法: %q，允许 memory/redis", c.Redis.Provider)
	}
	return nil
}

/*
 * validateAuth 校验密钥强度与环境要求。
 * 口径：HMAC 密钥至少 32 字节；staging/prod 必须注入固定签名种子以支持多实例校验。
 */
func (c Config) validateAuth() error {
	if len(c.Auth.HMACSecret) < 32 {
		return fmt.Errorf("GN_HMAC_SECRET 至少 32 字节")
	}
	if c.Env != "dev" && c.Auth.HMACSecret == devHMACSecret {
		return fmt.Errorf("GN_HMAC_SECRET 在 %s 必须显式配置", c.Env)
	}
	if c.Env != "dev" && len(c.Auth.SigningSeed) == 0 {
		return fmt.Errorf("GN_JWT_PRIVATE_KEY 在 %s 环境必须配置", c.Env)
	}
	return nil
}

/* decodeSigningSeed 解析 base64 的 Ed25519 种子；空值返回 nil，由签发器生成一次性密钥 */
func decodeSigningSeed(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
	}
	if err != nil {
		return nil, fmt.Errorf("解析 GN_JWT_PRIVATE_KEY: %w", err)
	}
	if len(decoded) != ed25519.SeedSize {
		return nil, fmt.Errorf("GN_JWT_PRIVATE_KEY 必须为 %d 字节种子（base64）", ed25519.SeedSize)
	}
	return decoded, nil
}

/*
 * validateS3 只在选择 MinIO/S3 适配器时要求连接参数。
 * 使用 fake 的单元测试与离线环境因此无需准备任何凭据。
 */
func (c Config) validateS3() error {
	switch strings.TrimSpace(strings.ToLower(c.Providers.ObjectStore)) {
	case "minio", "s3":
	default:
		return nil
	}
	required := []struct {
		name  string
		value string
	}{
		{"GN_S3_ENDPOINT", c.S3.Endpoint},
		{"GN_S3_BUCKET", c.S3.Bucket},
		{"GN_S3_ACCESS_KEY", c.S3.AccessKey},
		{"GN_S3_SECRET_KEY", c.S3.SecretKey},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s 在对象存储使用 minio 时不能为空", item.name)
		}
	}
	return nil
}

/* SlogLevel 把日志级别配置映射为 slog 级别，未知值回落为 info */
func (c Config) SlogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

/* getEnv 读取环境变量，空值与纯空白视为未配置 */
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
