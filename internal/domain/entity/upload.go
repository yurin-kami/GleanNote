/*
 * 上传会话实体：30 分钟有效期的直传凭据与完成校验结果。
 *
 * 职责：承载对象键、声明元数据、校验状态与缩略图键，并约束状态迁移。
 * 不负责：对象存储读写、SHA256 计算、图片解码与缩略图生成。
 * 依赖：标准库。
 * 参考：docs/API.md#541-post-v1uploads、docs/API.md#542-post-v1uploadsuploadidcomplete
 */
package entity

import "time"

/* UploadStatus 是上传会话状态，取值见 docs/API.md#32-枚举字典 */
type UploadStatus string

/* PENDING 待上传；UPLOADED 已上传未校验；READY 校验与缩略图完成；USED 已被消费；EXPIRED 会话超时 */
const (
	UploadStatusPending  UploadStatus = "PENDING"
	UploadStatusUploaded UploadStatus = "UPLOADED"
	UploadStatusReady    UploadStatus = "READY"
	UploadStatusUsed     UploadStatus = "USED"
	UploadStatusExpired  UploadStatus = "EXPIRED"
)

/*
 * Upload 是一次图片直传会话。
 * 技术字段：SHA256 为客户端声明值，Bytes 用于容量计量与完整性校验，
 * ThumbKey 指向服务端生成的缩略图对象。
 */
type Upload struct {
	ID          string
	UserID      string
	ObjectKey   string
	FileName    string
	ContentType string
	SHA256      string
	Bytes       int64
	Width       int
	Height      int
	ThumbKey    string
	Status      UploadStatus
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

/* NewUpload 创建待上传会话；expiresAt 由用例按 30 分钟会话有效期计算 */
func NewUpload(id, userID, objectKey, fileName, contentType, sha256 string,
	bytes int64, expiresAt, now time.Time) *Upload {
	return &Upload{
		ID:          id,
		UserID:      userID,
		ObjectKey:   objectKey,
		FileName:    fileName,
		ContentType: contentType,
		SHA256:      sha256,
		Bytes:       bytes,
		Status:      UploadStatusPending,
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}
}

/* IsReady 判断会话是否已通过校验并生成缩略图，供创建笔记时复用 */
func (u *Upload) IsReady() bool { return u.Status == UploadStatusReady }

/* Consumed 表示会话已被某次建页或追加消费，同一 uploadId 不允许重复使用 */
func (u *Upload) Consumed() bool { return u.Status == UploadStatusUsed }

/*
 * Expired 判断会话是否已超时。
 * 已 READY 的会话不受 30 分钟窗口约束，否则用户在有效期最后时刻完成也会不可用。
 * 已消费的会话按状态而不是时间判定，避免复用时被误报为「会话超时」。
 */
func (u *Upload) Expired(now time.Time) bool {
	switch u.Status {
	case UploadStatusPending, UploadStatusUploaded:
		return !now.Before(u.ExpiresAt)
	default:
		return false
	}
}

/* MarkExpired 把超时会话置为 EXPIRED，便于后续查询与清理任务识别 */
func (u *Upload) MarkExpired() { u.Status = UploadStatusExpired }

/*
 * MarkReady 记录校验结果并置为 READY。
 * 约束：width/height 来自解码结果，thumbKey 必须指向已写入的缩略图对象。
 */
func (u *Upload) MarkReady(width, height int, thumbKey, sha256 string) {
	u.Status = UploadStatusReady
	u.Width = width
	u.Height = height
	u.ThumbKey = thumbKey
	u.SHA256 = sha256
}
