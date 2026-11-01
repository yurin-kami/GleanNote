/* 上传用例：申请预签名会话与确认上传（字节、SHA256、解码与缩略图校验） */
package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* thumbKeyPrefix 是缩略图对象键前缀，桶内按前缀区分原图与派生图 */
const thumbKeyPrefix = "thumb/"

/*
 * CreateUploads 申请 1–20 个上传会话并返回预签名地址。
 * 顺序：校验声明元数据 → 逐个预签名（事务外）→ 批量写入会话。
 * 参考：docs/API.md#541-post-v1uploads
 */
func (s *Service) CreateUploads(ctx context.Context, userID string, items []UploadItem) ([]UploadTicket, error) {
	if err := requireUser(userID); err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > valueobject.MaxUploadBatch {
		return nil, errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("单次上传数量必须在 1–%d 之间", valueobject.MaxUploadBatch))
	}

	now := s.clock.Now()
	expiresAt := now.Add(UploadSessionTTL)
	uploads := make([]*entity.Upload, 0, len(items))
	tickets := make([]UploadTicket, 0, len(items))
	for index, item := range items {
		contentType, err := valueobject.ParseImageContentType(item.ContentType)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.FileName) == "" {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf("items[%d].fileName 必填", index))
		}
		if err := valueobject.ValidateImageBytes(item.Bytes); err != nil {
			return nil, err
		}
		sha, err := valueobject.ParseSHA256(item.SHA256)
		if err != nil {
			return nil, err
		}

		id, err := s.newID()
		if err != nil {
			return nil, err
		}
		objectKey := buildObjectKey(id, contentType, now)
		target, err := s.deps.Objects.PresignPut(ctx, objectKey, contentType.String(), UploadSessionTTL)
		if err != nil {
			return nil, errs.New(errs.CodeInternal, "生成上传地址失败").WithCause(err)
		}
		uploads = append(uploads, entity.NewUpload(id, userID, objectKey, item.FileName,
			contentType.String(), sha, item.Bytes, expiresAt, now))
		tickets = append(tickets, UploadTicket{
			UploadID:  id,
			ObjectKey: objectKey,
			PutURL:    target.URL,
			Headers:   target.Headers,
			ExpiresAt: expiresAt,
		})
	}

	if err := s.deps.Uploads.CreateBatch(ctx, uploads); err != nil {
		return nil, err
	}
	return tickets, nil
}

/*
 * CompleteUpload 确认对象已上传并完成校验。
 * 顺序：会话有效期 → 对象存在与字节数 → SHA256 → 解码与像素上限 → 写入缩略图 → 事务落状态。
 * 参考：docs/API.md#542-post-v1uploadsuploadidcomplete、docs/ARCHITECTURE.md#56-页面图片管线
 */
func (s *Service) CompleteUpload(ctx context.Context, userID, uploadID, requestSHA string) (UploadResult, error) {
	if err := requireUser(userID); err != nil {
		return UploadResult{}, err
	}
	sha, err := valueobject.ParseSHA256(requestSHA)
	if err != nil {
		return UploadResult{}, err
	}

	now := s.clock.Now()
	upload, err := s.deps.Uploads.GetByID(ctx, userID, uploadID)
	if err != nil {
		return UploadResult{}, err
	}
	if upload.Expired(now) {
		upload.MarkExpired()
		if err := s.deps.Uploads.Update(ctx, upload); err != nil {
			return UploadResult{}, err
		}
		return UploadResult{}, errs.New(errs.CodeUploadExpired, "上传会话已超时")
	}

	/* 幂等：已 READY 的会话直接返回既有结果，避免重复解码与重复写缩略图 */
	if upload.IsReady() {
		if upload.SHA256 != sha {
			return UploadResult{}, errs.New(errs.CodeUploadHashMismatch, "SHA256 与声明不一致")
		}
		return s.readyResult(ctx, upload)
	}
	if upload.SHA256 != sha {
		return UploadResult{}, errs.New(errs.CodeUploadHashMismatch, "SHA256 与声明不一致")
	}

	info, err := s.deps.Objects.Stat(ctx, upload.ObjectKey)
	if err != nil {
		if errors.Is(err, port.ErrObjectNotFound) {
			return UploadResult{}, errs.New(errs.CodeUploadSizeMismatch, "对象不存在或尚未上传完成")
		}
		return UploadResult{}, errs.New(errs.CodeInternal, "读取对象元数据失败").WithCause(err)
	}
	if info.Size != upload.Bytes {
		return UploadResult{}, errs.New(errs.CodeUploadSizeMismatch, "实际字节数与声明不一致")
	}

	data, err := s.deps.Objects.Get(ctx, upload.ObjectKey)
	if err != nil {
		if errors.Is(err, port.ErrObjectNotFound) {
			return UploadResult{}, errs.New(errs.CodeUploadSizeMismatch, "对象不存在或尚未上传完成")
		}
		return UploadResult{}, errs.New(errs.CodeInternal, "读取对象失败").WithCause(err)
	}
	digest := sha256.Sum256(data)
	computed := hex.EncodeToString(digest[:])
	if !strings.EqualFold(computed, sha) {
		return UploadResult{}, errs.New(errs.CodeUploadHashMismatch, "对象内容 SHA256 与声明不一致")
	}

	inspection, err := s.deps.Images.Inspect(ctx, data)
	if err != nil {
		if errors.Is(err, port.ErrImageUnsupported) {
			return UploadResult{}, errs.New(errs.CodeUploadUnsupportedType, "仅支持 JPG、PNG、WEBP 格式").WithCause(err)
		}
		return UploadResult{}, errs.New(errs.CodeInternal, "图片处理失败").WithCause(err)
	}
	if !formatMatches(upload.ContentType, inspection.Format) {
		return UploadResult{}, errs.New(errs.CodeUploadUnsupportedType, "声明类型与图片实际格式不一致")
	}

	thumbKey := thumbKeyPrefix + upload.ID + ".jpg"
	if err := s.deps.Objects.Put(ctx, thumbKey, inspection.ThumbnailContentType, inspection.Thumbnail); err != nil {
		return UploadResult{}, errs.New(errs.CodeInternal, "写入缩略图失败").WithCause(err)
	}

	var ready *entity.Upload
	err = s.deps.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		current, err := s.deps.Uploads.GetByID(txCtx, userID, uploadID)
		if err != nil {
			return err
		}
		if current.Expired(s.clock.Now()) {
			return errs.New(errs.CodeUploadExpired, "上传会话已超时")
		}
		if !current.IsReady() {
			current.MarkReady(inspection.Width, inspection.Height, thumbKey, computed)
			if err := s.deps.Uploads.Update(txCtx, current); err != nil {
				return err
			}
		}
		ready = current
		return nil
	})
	if err != nil {
		return UploadResult{}, err
	}
	return s.readyResult(ctx, ready)
}

/* readyResult 生成完成结果并签发 10 分钟有效的缩略图地址 */
func (s *Service) readyResult(ctx context.Context, upload *entity.Upload) (UploadResult, error) {
	thumbURL, _, err := s.deps.Objects.PresignGet(ctx, upload.ThumbKey, MediaURLTTL)
	if err != nil {
		return UploadResult{}, errs.New(errs.CodeInternal, "生成缩略图地址失败").WithCause(err)
	}
	return UploadResult{
		UploadID: upload.ID,
		Status:   string(upload.Status),
		Width:    upload.Width,
		Height:   upload.Height,
		Bytes:    upload.Bytes,
		ThumbURL: thumbURL,
	}, nil
}

/*
 * buildObjectKey 生成对象键：u/{id 前 4 位}/{yyyy}/{mm}/{id}{扩展名}。
 * 前缀带日期便于按生命周期规则清理；键与用户解耦，避免暴露账号信息。
 */
func buildObjectKey(uploadID string, contentType valueobject.ImageContentType, at time.Time) string {
	prefix := uploadID
	if len(prefix) > 4 {
		prefix = prefix[:4]
	}
	return fmt.Sprintf("u/%s/%s/%s%s", prefix, at.UTC().Format("2006/01"), uploadID, contentType.Extension())
}

/* formatMatches 校验声明类型与解码器识别出的格式一致，防止扩展名伪装 */
func formatMatches(contentType string, format string) bool {
	switch valueobject.ImageContentType(contentType) {
	case valueobject.ImageJPEG:
		return format == "jpeg"
	case valueobject.ImagePNG:
		return format == "png"
	case valueobject.ImageWebP:
		return format == "webp"
	default:
		return false
	}
}
