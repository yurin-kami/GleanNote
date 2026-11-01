/*
 * 配置测试：校验认证密钥的解析与环境保护。
 * 规则来源：docs/ARCHITECTURE.md#74-环境与配置、docs/CONVENTIONS.md#31-通用 G-06。
 */
package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

/* TestDecodeSigningSeedAcceptsEmptyAndValid 校验空值表示一次性密钥，合法种子原样解码 */
func TestDecodeSigningSeedAcceptsEmptyAndValid(t *testing.T) {
	if seed, err := decodeSigningSeed(""); err != nil || seed != nil {
		t.Fatalf("空值应返回 nil,nil，实际 %v,%v", seed, err)
	}

	raw := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	seed, err := decodeSigningSeed(raw)
	if err != nil || len(seed) != 32 {
		t.Fatalf("合法种子解码失败: %v，长度 %d", err, len(seed))
	}
}

/* TestDecodeSigningSeedRejectsInvalid 校验非法 base64 与错误长度都必须启动失败 */
func TestDecodeSigningSeedRejectsInvalid(t *testing.T) {
	if _, err := decodeSigningSeed("not-base64!"); err == nil {
		t.Fatalf("非法 base64 应返回错误")
	}
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := decodeSigningSeed(short); err == nil {
		t.Fatalf("错误长度应返回错误")
	}
}

/* TestValidateAuthRequiresStrongSecret 校验 HMAC 密钥长度下限 */
func TestValidateAuthRequiresStrongSecret(t *testing.T) {
	cfg := Config{Env: "dev", Auth: AuthConfig{HMACSecret: "short"}}
	if err := cfg.validateAuth(); err == nil {
		t.Fatalf("过短密钥应返回错误")
	}
}

/* TestValidateAuthRejectsDevSecretInProd 校验生产环境禁止使用内置默认密钥 */
func TestValidateAuthRejectsDevSecretInProd(t *testing.T) {
	cfg := Config{Env: "prod", Auth: AuthConfig{HMACSecret: devHMACSecret, SigningSeed: make([]byte, 32)}}
	if err := cfg.validateAuth(); err == nil {
		t.Fatalf("生产环境使用默认密钥应返回错误")
	}
}

/* TestValidateAuthRequiresSeedOutsideDev 校验 staging/prod 必须注入固定签名种子 */
func TestValidateAuthRequiresSeedOutsideDev(t *testing.T) {
	cfg := Config{Env: "staging", Auth: AuthConfig{HMACSecret: strings.Repeat("k", 32)}}
	if err := cfg.validateAuth(); err == nil {
		t.Fatalf("staging 缺少签名种子应返回错误")
	}

	cfg.Auth.SigningSeed = make([]byte, 32)
	if err := cfg.validateAuth(); err != nil {
		t.Fatalf("staging 配置完整不应报错: %v", err)
	}
}

/* TestValidateVision 校验视觉提供者名称与 deepseek 的必填凭据 */
func TestValidateVision(t *testing.T) {
	cfg := Config{Providers: Providers{Vision: "deepseek"}}
	if err := cfg.validateVision(); err == nil {
		t.Fatalf("deepseek 缺少 API Key 应返回错误")
	}

	cfg.Vision.APIKey = "test-key"
	if err := cfg.validateVision(); err != nil {
		t.Fatalf("配置完整不应报错: %v", err)
	}

	cfg.Providers.Vision = "vendor-x"
	if err := cfg.validateVision(); err == nil {
		t.Fatalf("未注册的视觉提供者应返回错误")
	}
}
