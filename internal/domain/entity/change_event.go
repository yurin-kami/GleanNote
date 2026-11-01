/*
 * 变更日志实体：增量同步的事件源。
 *
 * 职责：固化事件序号、资源类型、操作与投影载荷，作为客户端回放依据。
 * 不负责：序号分配、保留期清理与分页过滤（由仓储与用例负责）。
 * 依赖：标准库。
 * 参考：docs/API.md#510-增量同步、docs/ARCHITECTURE.md#65-增量同步
 */
package entity

import "time"

/* ChangeResourceType 是同步资源类型，取值与契约一致 */
type ChangeResourceType string

/* 同步资源类型：目录、笔记、标签与页面 */
const (
	ChangeResourceFolder ChangeResourceType = "FOLDER"
	ChangeResourceNote   ChangeResourceType = "NOTE"
	ChangeResourceTag    ChangeResourceType = "TAG"
	ChangeResourcePage   ChangeResourceType = "PAGE"
)

/* ChangeOperation 是变更操作，DELETE 为墓碑事件 */
type ChangeOperation string

/* 变更操作：新建、更新与删除 */
const (
	ChangeOpCreate ChangeOperation = "CREATE"
	ChangeOpUpdate ChangeOperation = "UPDATE"
	ChangeOpDelete ChangeOperation = "DELETE"
)

/* ValidChangeResourceType 判断资源类型是否在枚举内 */
func ValidChangeResourceType(value ChangeResourceType) bool {
	switch value {
	case ChangeResourceFolder, ChangeResourceNote, ChangeResourceTag, ChangeResourcePage:
		return true
	default:
		return false
	}
}

/*
 * ChangeEvent 是一条变更记录。
 * 约束：Seq 在账号内严格单调；Op=DELETE 时 Payload 必须为 nil（墓碑事件）。
 */
type ChangeEvent struct {
	Seq          int64
	UserID       string
	ResourceType ChangeResourceType
	ResourceID   string
	Op           ChangeOperation
	Revision     int
	UpdatedAt    time.Time
	Payload      map[string]any
}

/* IsTombstone 判断是否为墓碑事件 */
func (e *ChangeEvent) IsTombstone() bool { return e.Op == ChangeOpDelete }

/* ChangeResourceTypes 返回全部同步资源类型，供缺省过滤使用 */
func ChangeResourceTypes() []ChangeResourceType {
	return []ChangeResourceType{
		ChangeResourceFolder, ChangeResourceNote, ChangeResourceTag, ChangeResourcePage,
	}
}
