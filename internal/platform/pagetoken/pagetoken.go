/*
 * Package pagetoken 生成与校验 AIP-158 的不透明分页令牌。
 *
 * 职责：把排序键与资源 ID 封装为带 HMAC 签名、带有效期、按端点隔离的令牌。
 * 不负责：分页查询本身、排序白名单校验（由用例与传输层负责）。
 * 依赖：platform/clock、标准库。
 * 参考：docs/ARCHITECTURE.md#83-分页过滤与排序、docs/API.md#24-分页
 */
package pagetoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/platform/clock"
)

/*
 * 令牌校验的两类失败。
 * 两者对外都映射为 INVALID_PAGE_TOKEN，区分开是为了日志与测试。
 */
var (
	ErrInvalid = errors.New("pagetoken: invalid")
	ErrExpired = errors.New("pagetoken: expired")
)

/* payload 是令牌载荷；字段名缩写以缩短令牌长度 */
type payload struct {
	Version   int    `json:"v"`
	Scope     string `json:"s"`
	SortKey   string `json:"k"`
	ID        string `json:"i"`
	ExpiresAt int64  `json:"e"`
}

/*
 * Payload 是解码后的分页位置。
 * 调用方必须用 Scope 对应的端点解码，禁止跨端点复用。
 */
type Payload struct {
	Scope   string
	SortKey string
	ID      string
}

/*
 * Codec 负责令牌的签发与校验。
 * Secret 少于 32 字节时构造失败，避免弱密钥。
 */
type Codec struct {
	secret []byte
	ttl    time.Duration
	clock  clock.Clock
}

/* NewCodec 创建编解码器；secret 建议直接使用 32 字节以上的随机密钥 */
func NewCodec(secret []byte, ttl time.Duration, now clock.Clock) (*Codec, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("分页令牌密钥至少 32 字节，实际 %d", len(secret))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("分页令牌有效期必须为正数: %s", ttl)
	}
	if now == nil {
		return nil, errors.New("分页令牌编解码器缺少时钟")
	}
	copied := make([]byte, len(secret))
	copy(copied, secret)
	return &Codec{secret: copied, ttl: ttl, clock: now}, nil
}

/*
 * Encode 以 scope + 排序键 + 资源 ID 生成令牌。
 * 签名覆盖 scope，防止把笔记列表的令牌拿到回收站列表复用。
 */
func (c *Codec) Encode(scope, sortKey, id string) (string, error) {
	body, err := json.Marshal(payload{
		Version:   1,
		Scope:     scope,
		SortKey:   sortKey,
		ID:        id,
		ExpiresAt: c.clock.Now().Add(c.ttl).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("序列化分页载荷: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	signature := c.sign(scope, encoded)
	return encoded + "." + signature, nil
}

/*
 * Decode 校验签名、作用域与有效期后返回分页位置。
 * 校验顺序固定为「签名 → 作用域 → 有效期」，避免对未验签数据做业务判断。
 */
func (c *Codec) Decode(scope, token string) (Payload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Payload{}, ErrInvalid
	}
	if !hmac.Equal([]byte(parts[1]), []byte(c.sign(scope, parts[0]))) {
		return Payload{}, ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Payload{}, ErrInvalid
	}
	var decoded payload
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Payload{}, ErrInvalid
	}
	if decoded.Version != 1 || decoded.Scope != scope {
		return Payload{}, ErrInvalid
	}
	if c.clock.Now().Unix() >= decoded.ExpiresAt {
		return Payload{}, ErrExpired
	}
	return Payload{Scope: decoded.Scope, SortKey: decoded.SortKey, ID: decoded.ID}, nil
}

/* sign 计算 scope 与载荷的 HMAC-SHA256，输出无填充 Base64URL */
func (c *Codec) sign(scope, encodedPayload string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(scope))
	mac.Write([]byte{0x00})
	mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
