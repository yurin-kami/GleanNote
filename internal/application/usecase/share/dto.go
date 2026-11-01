/* 分享用例的输入输出类型；公开页只输出标题、正文与页面原图 */
package share

import "time"

/* ShareLinkView 是分享状态；未开启时 Enabled=false 且 URL 为空 */
type ShareLinkView struct {
	Enabled   bool
	URL       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

/* PublicPageView 是公开页的单页图片地址 */
type PublicPageView struct {
	Index       int
	OriginalURL string
}

/* PublicNoteView 是匿名公开读取的只读内容 */
type PublicNoteView struct {
	Title     string
	Content   string
	Pages     []PublicPageView
	UpdatedAt time.Time
}
