/*
 * Package idgen 生成全局唯一、时间有序的资源标识符。
 *
 * 职责：产出 UUIDv7 字符串；实现可替换，便于测试注入确定性 ID。
 * 不负责：业务 ID 的语义校验、请求 ID 生成（见 platform/requestid）。
 * 依赖：仅 uuid 库与标准库。
 * 参考：docs/ARCHITECTURE.md#81-资源命名与路径、docs/API.md#21-基础约定
 */
package idgen

import "github.com/google/uuid"

/*
 * Generator 是 ID 生成端口。
 * 返回错误表示系统随机源不可用，调用方必须终止本次写入而不是复用旧 ID。
 */
type Generator interface {
	New() (string, error)
}

/* UUIDv7 是生产实现：前 48 位为毫秒时间戳，天然按创建顺序排列 */
type UUIDv7 struct{}

/*
 * New 生成一个 UUIDv7 字符串。
 * UUIDv7 的时间有序特性让 B-Tree 索引写入集中在尾部，避免随机写放大。
 */
func (UUIDv7) New() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
