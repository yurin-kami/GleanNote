/*
 * Package password 实现 OWASP 推荐的口令哈希：Argon2id。
 *
 * 职责：生成带随机盐的编码串、校验明文与编码串是否匹配。
 * 不负责：密码策略校验（见 domain/valueobject）与用户存储。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权、OWASP Password Storage Cheat Sheet
 * <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>
 */
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

/*
 * 参数固定为 m=64MiB、t=3、p=2，与架构文档一致。
 * 参数写入编码串，未来调参时旧哈希仍可校验。
 */
const (
	memoryKiB   = 64 * 1024
	iterations  = 3
	parallelism = 2
	saltLength  = 16
	keyLength   = 32
	encodedID   = "argon2id"
)

/* ErrMalformed 表示编码串格式非法，调用方应视为校验失败而非重试 */
var ErrMalformed = errors.New("password: malformed encoded hash")

/* Argon2id 是 port.PasswordHasher 的生产实现，并发安全且无状态 */
type Argon2id struct{}

/* NewArgon2id 创建哈希器 */
func NewArgon2id() *Argon2id {
	return &Argon2id{}
}

/*
 * Hash 生成形如 $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash> 的编码串。
 * 每次调用使用新的 16 字节随机盐，同一明文两次结果不同。
 */
func (a *Argon2id) Hash(ctx context.Context, plain string) (string, error) {
	if plain == "" {
		return "", errors.New("password: 明文不能为空")
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: 生成随机盐: %w", err)
	}
	digest := argon2.IDKey([]byte(plain), salt, iterations, memoryKiB, parallelism, keyLength)
	encoded := fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		encodedID, argon2.Version, memoryKiB, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest),
	)
	return encoded, nil
}

/*
 * Verify 以常量时间比较派生结果。
 * 编码串损坏时返回错误；参数不匹配时返回 false, nil，避免把口令错误暴露成系统故障。
 */
func (a *Argon2id) Verify(ctx context.Context, encoded, plain string) (bool, error) {
	params, salt, expected, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if plain == "" {
		return false, nil
	}
	actual := argon2.IDKey([]byte(plain), salt, params.iterations, params.memory, params.parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

/* argonParams 是编码串中解析出的派生参数 */
type argonParams struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

/* decode 解析编码串；格式或参数非法统一返回 ErrMalformed */
func decode(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != encodedID {
		return argonParams{}, nil, nil, ErrMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return argonParams{}, nil, nil, ErrMalformed
	}
	var params argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.iterations, &params.parallelism); err != nil {
		return argonParams{}, nil, nil, ErrMalformed
	}
	if params.memory == 0 || params.iterations == 0 || params.parallelism == 0 {
		return argonParams{}, nil, nil, ErrMalformed
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return argonParams{}, nil, nil, ErrMalformed
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(digest) < 16 {
		return argonParams{}, nil, nil, ErrMalformed
	}
	return params, salt, digest, nil
}
