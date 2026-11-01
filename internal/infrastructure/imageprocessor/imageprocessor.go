/*
 * Package imageprocessor 提供原图校验与缩略图生成。
 *
 * 职责：先读头部限制像素总数，再解码并按长边 320 生成 JPEG 缩略图。
 * 不负责：对象存储读写、SHA256 计算与格式声明校验（由上传用例比较 Format 与声明类型）。
 * 依赖：application/port、domain/valueobject、golang.org/x/image。
 * 参考：docs/ARCHITECTURE.md#56-页面图片管线、docs/ARCHITECTURE.md#8.12-安全与隐私
 */
package imageprocessor

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* thumbnailQuality 是缩略图 JPEG 质量；列表页只展示小图，82 足以保持可读 */
const thumbnailQuality = 82

/* Processor 是 port.ImageProcessor 的默认实现，无状态可并发调用 */
type Processor struct{}

/* New 创建图片处理器 */
func New() *Processor { return &Processor{} }

/*
 * Inspect 校验图片并生成缩略图。
 * 顺序：头部解码 → 像素上限 → 完整解码 → 长边缩放 → JPEG 编码，
 * 任何一步失败统一返回 port.ErrImageUnsupported，避免泄漏供应商细节。
 */
func (Processor) Inspect(ctx context.Context, data []byte) (port.ImageInspection, error) {
	if err := ctx.Err(); err != nil {
		return port.ImageInspection{}, err
	}
	if len(data) == 0 {
		return port.ImageInspection{}, fmt.Errorf("%w: 内容为空", port.ErrImageUnsupported)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return port.ImageInspection{}, fmt.Errorf("%w: 无法解析图片头: %w", port.ErrImageUnsupported, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return port.ImageInspection{}, fmt.Errorf("%w: 尺寸非法 %dx%d", port.ErrImageUnsupported, config.Width, config.Height)
	}
	if !valueobject.PixelCountWithinLimit(config.Width, config.Height) {
		return port.ImageInspection{}, fmt.Errorf("%w: 像素总数超过上限", port.ErrImageUnsupported)
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return port.ImageInspection{}, fmt.Errorf("%w: 解码失败: %w", port.ErrImageUnsupported, err)
	}
	if err := ctx.Err(); err != nil {
		return port.ImageInspection{}, err
	}

	thumbnail, err := encodeThumbnail(decoded)
	if err != nil {
		return port.ImageInspection{}, err
	}
	return port.ImageInspection{
		Width:                config.Width,
		Height:               config.Height,
		Format:               format,
		Thumbnail:            thumbnail,
		ThumbnailContentType: "image/jpeg",
	}, nil
}

/*
 * encodeThumbnail 把图片缩放到长边 320 并编码为 JPEG。
 * 小于 320 的图片按原尺寸重新编码，保证输出类型统一，客户端无需区分。
 */
func encodeThumbnail(source image.Image) ([]byte, error) {
	bounds := source.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	longEdge := width
	if height > longEdge {
		longEdge = height
	}
	scale := 1.0
	if longEdge > valueobject.ThumbLongEdge {
		scale = float64(valueobject.ThumbLongEdge) / float64(longEdge)
	}
	targetWidth := maxInt(1, int(math.Round(float64(width)*scale)))
	targetHeight := maxInt(1, int(math.Round(float64(height)*scale)))

	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(target, target.Bounds(), source, bounds, draw.Src, nil)

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, target, &jpeg.Options{Quality: thumbnailQuality}); err != nil {
		return nil, fmt.Errorf("编码缩略图: %w", err)
	}
	return buffer.Bytes(), nil
}

/* maxInt 返回较大值，用于兜底保证缩放后尺寸至少为 1 像素 */
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
