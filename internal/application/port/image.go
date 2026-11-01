/*
 * 图片处理能力的端口。
 *
 * 职责：把「解码、像素上限校验、生成缩略图」抽象为一处，业务层只依赖结果。
 * 不负责：对象存储读写、SHA256 计算（由上传用例完成）。
 * 依赖：标准库。
 * 参考：docs/ARCHITECTURE.md#56-页面图片管线
 */
package port

import (
	"context"
	"errors"
)

/* 图片处理失败只有一种对外语义：格式无法识别或超出像素上限 */
var ErrImageUnsupported = errors.New("image unsupported")

/*
 * ImageInspection 是解码结果。
 * Format 为解码器识别出的格式名（jpeg/png/webp），供用例校验声明类型与魔数一致；
 * Thumbnail 为已编码的缩略图字节，ThumbnailContentType 供对象存储写入时使用。
 */
type ImageInspection struct {
	Width                int
	Height               int
	Format               string
	Thumbnail            []byte
	ThumbnailContentType string
}

/*
 * ImageProcessor 校验原图并生成缩略图。
 * 约束：实现必须在解码前限制读取规模，避免解压炸弹；失败统一返回 ErrImageUnsupported。
 */
type ImageProcessor interface {
	Inspect(ctx context.Context, data []byte) (ImageInspection, error)
}
