/*
 * 图片值对象：上传格式、大小、摘要与像素上限的统一校验口径。
 *
 * 职责：把契约中的「仅 JPG/PNG/WEBP、单张 ≤10MB、≤4000 万像素」收敛为可复用函数。
 * 不负责：实际解码与缩略图生成（见 infrastructure/imageprocessor）、对象存储读写。
 * 依赖：标准库、domain/errs。
 * 参考：docs/API.md#29-图片上传协议、docs/ARCHITECTURE.md#56-页面图片管线
 */
package valueobject

import (
	"fmt"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 图片校验上限，与 PRD 5.2 和架构文档 5.6 的管线约定一致 */
const (
	MaxImageBytes  int64 = 10 * 1024 * 1024
	MaxImagePixels int64 = 40_000_000
	ThumbLongEdge        = 320
)

/* ImageContentType 是允许上传的图片媒体类型 */
type ImageContentType string

/* 仅接受三种格式；HEIC 等由客户端预先转码（PRD FR-04） */
const (
	ImageJPEG ImageContentType = "image/jpeg"
	ImagePNG  ImageContentType = "image/png"
	ImageWebP ImageContentType = "image/webp"
)

/* ParseImageContentType 校验并归一媒体类型；空值与未知类型返回校验错误 */
func ParseImageContentType(raw string) (ImageContentType, error) {
	value := ImageContentType(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case ImageJPEG, ImagePNG, ImageWebP:
		return value, nil
	default:
		return "", errs.New(errs.CodeValidationFailed, "contentType 只支持 image/jpeg、image/png、image/webp")
	}
}

/* String 返回媒体类型原文 */
func (c ImageContentType) String() string { return string(c) }

/* Extension 返回对象键使用的扩展名；未知类型返回空串 */
func (c ImageContentType) Extension() string {
	switch c {
	case ImageJPEG:
		return ".jpg"
	case ImagePNG:
		return ".png"
	case ImageWebP:
		return ".webp"
	default:
		return ""
	}
}

/*
 * ParseSHA256 校验客户端声明的摘要。
 * 约束：固定 64 位小写十六进制，与 openapi 的 pattern 一致。
 */
func ParseSHA256(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) != 64 {
		return "", errs.New(errs.CodeValidationFailed, "sha256 必须为 64 位十六进制")
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", errs.New(errs.CodeValidationFailed, "sha256 只能包含小写十六进制字符")
		}
	}
	return value, nil
}

/* ValidateImageBytes 校验声明字节数；超出 10MB 直接拒绝，避免为超限对象签名 */
func ValidateImageBytes(bytes int64) error {
	if bytes <= 0 || bytes > MaxImageBytes {
		return errs.New(errs.CodeValidationFailed,
			fmt.Sprintf("bytes 必须在 1–%d 之间", MaxImageBytes))
	}
	return nil
}

/*
 * PixelCountWithinLimit 判断解码后的像素总数是否在上限内。
 * 用途：complete 阶段的解压炸弹防护，必须在校验通过后才生成缩略图。
 */
func PixelCountWithinLimit(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	return int64(width)*int64(height) <= MaxImagePixels
}
