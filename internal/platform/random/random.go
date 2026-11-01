/*
 * Package random 封装密码学安全的随机串生成。
 *
 * 职责：生成不可预测的不透明令牌与数字验证码。
 * 不负责：令牌哈希与存储（由调用方按端口处理）。
 * 依赖：crypto/rand 与 math/big。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权
 */
package random

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
)

/*
 * Token 生成 n 字节随机数据并以无填充 Base64URL 编码。
 * 输出长度约为 4n/3，可直接作为刷新令牌、请求 ID 等高熵标识。
 */
func Token(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("随机字节数必须为正数: %d", n)
	}
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("读取系统随机源: %w", err)
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(buffer), "="), nil
}

/*
 * Digits 生成恰好 n 位十进制数字，用于邮箱验证码。
 * 使用 crypto/rand 的区间抽样，避免取模引入的分布偏差。
 */
func Digits(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("验证码位数必须为正数: %d", n)
	}
	ten := big.NewInt(10)
	var builder strings.Builder
	builder.Grow(n)
	for index := 0; index < n; index++ {
		value, err := rand.Int(rand.Reader, ten)
		if err != nil {
			return "", fmt.Errorf("读取系统随机源: %w", err)
		}
		builder.WriteByte(byte('0' + value.Int64()))
	}
	return builder.String(), nil
}
