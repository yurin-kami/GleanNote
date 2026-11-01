/* 测试图片校验、格式识别与缩略图长边约束 */
package imageprocessor_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/infrastructure/imageprocessor"
)

/* encodeJPEG 生成指定尺寸的渐变图片并编码为 JPEG */
func encodeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 180, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, source, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("编码 JPEG 失败: %v", err)
	}
	return buffer.Bytes()
}

/* encodePNG 生成 PNG 字节，用于校验格式识别 */
func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, source); err != nil {
		t.Fatalf("编码 PNG 失败: %v", err)
	}
	return buffer.Bytes()
}

/* TestInspectScalesLongEdge 校验长度边缩放到 320 且保持比例 */
func TestInspectScalesLongEdge(t *testing.T) {
	processor := imageprocessor.New()
	inspection, err := processor.Inspect(context.Background(), encodeJPEG(t, 640, 480))
	if err != nil {
		t.Fatalf("校验图片失败: %v", err)
	}
	if inspection.Width != 640 || inspection.Height != 480 {
		t.Fatalf("尺寸 = %dx%d，期望 640x480", inspection.Width, inspection.Height)
	}
	if inspection.Format != "jpeg" {
		t.Fatalf("格式 = %q，期望 jpeg", inspection.Format)
	}
	if inspection.ThumbnailContentType != "image/jpeg" || len(inspection.Thumbnail) == 0 {
		t.Fatalf("缩略图类型 = %q，长度 = %d", inspection.ThumbnailContentType, len(inspection.Thumbnail))
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(inspection.Thumbnail))
	if err != nil {
		t.Fatalf("解析缩略图失败: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("缩略图格式 = %q，期望 jpeg", format)
	}
	if config.Width != 320 || config.Height != 240 {
		t.Fatalf("缩略图尺寸 = %dx%d，期望 320x240", config.Width, config.Height)
	}
}

/* TestInspectKeepsSmallImage 校验小于长边上限的图片按原尺寸输出 */
func TestInspectKeepsSmallImage(t *testing.T) {
	processor := imageprocessor.New()
	inspection, err := processor.Inspect(context.Background(), encodePNG(t, 100, 80))
	if err != nil {
		t.Fatalf("校验图片失败: %v", err)
	}
	if inspection.Format != "png" {
		t.Fatalf("格式 = %q，期望 png", inspection.Format)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(inspection.Thumbnail))
	if err != nil {
		t.Fatalf("解析缩略图失败: %v", err)
	}
	if config.Width != 100 || config.Height != 80 {
		t.Fatalf("缩略图尺寸 = %dx%d，期望 100x80", config.Width, config.Height)
	}
}

/* TestInspectRejectsInvalid 校验空内容与无法识别内容统一返回 ErrImageUnsupported */
func TestInspectRejectsInvalid(t *testing.T) {
	processor := imageprocessor.New()
	for name, data := range map[string][]byte{
		"空内容":  nil,
		"随机字节": []byte("\x00\x01\x02not-an-image"),
	} {
		if _, err := processor.Inspect(context.Background(), data); !errors.Is(err, port.ErrImageUnsupported) {
			t.Fatalf("%s 应返回 ErrImageUnsupported，实际 %v", name, err)
		}
	}
}

/* TestInspectHonorsContext 校验上下文取消后立即返回 */
func TestInspectHonorsContext(t *testing.T) {
	processor := imageprocessor.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := processor.Inspect(ctx, encodeJPEG(t, 10, 10)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，实际 %v", err)
	}
}
