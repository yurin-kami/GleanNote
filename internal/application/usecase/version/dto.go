/* 版本用例的输入输出类型；正文只在详情视图中出现 */
package version

import "time"

/* VersionView 是版本列表项，与 openapi 的 Version 保持一致 */
type VersionView struct {
	ID        string
	Source    string
	CharCount int
	CreatedAt time.Time
	ExpiresAt time.Time
}

/* VersionDetailView 是版本详情，额外携带正文 */
type VersionDetailView struct {
	VersionView
	Content string
}

/* ListVersionsResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListVersionsResult struct {
	Items         []VersionView
	NextPageToken string
}
