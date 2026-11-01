/*
 * 导出与导入任务的持久化端口。
 *
 * 职责：以任务实体为边界描述创建、按 ID 读取与状态更新。
 * 约束：所有方法必须携带 userID，跨用户访问按 RESOURCE_NOT_FOUND 处理（G-08）。
 * 参考：docs/API.md#511-导出与导入（p1）
 */
package port

import (
	"context"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/* ExportJobRepository 管理导出任务生命周期 */
type ExportJobRepository interface {
	Create(ctx context.Context, job *entity.ExportJob) error
	GetByID(ctx context.Context, userID, exportID string) (*entity.ExportJob, error)
	Update(ctx context.Context, job *entity.ExportJob) error
}

/* ImportJobRepository 管理批量导入任务及逐项状态 */
type ImportJobRepository interface {
	Create(ctx context.Context, job *entity.ImportJob) error
	GetByID(ctx context.Context, userID, importID string) (*entity.ImportJob, error)
	Update(ctx context.Context, job *entity.ImportJob) error
}

/*
 * PDFImage 是待嵌入 PDF 的页面图片。
 * 约束：仅嵌入适配器支持的格式，其余格式由调用方跳过。
 */
type PDFImage struct {
	Data        []byte
	Width       int
	Height      int
	ContentType string
}

/* PDFDocument 是一次 PDF 渲染的完整输入：标题、Markdown 正文与页面图片 */
type PDFDocument struct {
	Title   string
	Content string
	Images  []PDFImage
}

/*
 * PDFRenderer 把文档渲染为 PDF 字节。
 * 实现位于 infrastructure，用例只依赖本端口，避免应用层感知排版细节。
 */
type PDFRenderer interface {
	Render(ctx context.Context, document PDFDocument) ([]byte, error)
}
