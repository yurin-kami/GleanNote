/*
 * Package service 承载跨实体的纯领域规则：目录树的路径物化与展示。
 *
 * 职责：路径拼接、前缀判重、子树搬迁与面包屑展示，全部为无状态纯函数。
 * 不负责：仓储读写、事务、越权校验（见 application/usecase/folder）。
 * 依赖：domain/valueobject；不依赖框架、时钟与网络。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、ADR-013
 */
package service

import (
	"strings"
)

/* pathSeparator 是物化路径的分隔符 */
const pathSeparator = "/"

/*
 * BuildFolderPath 拼接物化路径。
 * parentPath 为空表示根级，返回 /{folderID}；否则在父路径后追加 ID。
 */
func BuildFolderPath(parentPath, folderID string) string {
	if parentPath == "" {
		return pathSeparator + folderID
	}
	return strings.TrimSuffix(parentPath, pathSeparator) + pathSeparator + folderID
}

/*
 * IsWithinSubtree 判断 candidatePath 是否位于 rootPath 的子树内（含自身）。
 * 前缀必须以「/」结尾对齐，避免 /a/b 误判为 /a/bc 的祖先。
 */
func IsWithinSubtree(rootPath, candidatePath string) bool {
	if rootPath == "" || candidatePath == "" {
		return false
	}
	if candidatePath == rootPath {
		return true
	}
	return strings.HasPrefix(candidatePath, rootPath+pathSeparator)
}

/*
 * RelocatePath 把 path 中 oldRootPath 前缀替换为 newRootPath。
 * 用于移动或恢复目录时批量重写子树路径；前缀必须按「/」对齐，避免误改 /a/bc。
 */
func RelocatePath(path, oldRootPath, newRootPath string) string {
	if !IsWithinSubtree(oldRootPath, path) {
		return path
	}
	return newRootPath + strings.TrimPrefix(path, oldRootPath)
}

/* DisplayPath 用「 / 」拼接面包屑，供客户端直接展示 */
func DisplayPath(names []string) string {
	return strings.Join(names, " / ")
}
