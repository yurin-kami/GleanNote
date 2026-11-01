/* 上传与页面读取的输入输出类型；HTTP DTO 由传输层独立定义 */
package upload

import "time"

/* UploadItem 是申请上传会话时客户端声明的图片元数据 */
type UploadItem struct {
	FileName    string
	ContentType string
	Bytes       int64
	SHA256      string
}

/* UploadTicket 是一次预签名上传凭据；Headers 必须由客户端原样携带 */
type UploadTicket struct {
	UploadID  string
	ObjectKey string
	PutURL    string
	Headers   map[string]string
	ExpiresAt time.Time
}

/* UploadResult 是完成校验后的会话结果 */
type UploadResult struct {
	UploadID string
	Status   string
	Width    int
	Height   int
	Bytes    int64
	ThumbURL string
}

/* PageView 是页面列表项；ThumbURL 为 10 分钟有效的缩略图地址 */
type PageView struct {
	ID        string
	Index     int
	Width     int
	Height    int
	Bytes     int64
	ThumbURL  string
	CreatedAt time.Time
}

/* ListPagesResult 是 AIP-158 集合响应；NextPageToken 为空表示没有下一页 */
type ListPagesResult struct {
	Items         []PageView
	NextPageToken string
}

/* PageDetail 是单页原图地址，ExpiresAt 为地址过期时间 */
type PageDetail struct {
	ID          string
	Index       int
	OriginalURL string
	ExpiresAt   time.Time
}
