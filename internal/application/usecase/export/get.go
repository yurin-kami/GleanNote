/* 导出结果查询：成功态每次读取都重新签发 1 小时有效的下载地址 */
package export

import (
	"context"
	"strings"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * GetExport 返回导出任务状态。
 * 规则：成功态重新签发下载地址，避免超过 1 小时后链接不可用；失败态只回失败码。
 */
func (s *Service) GetExport(ctx context.Context, userID, exportID string) (ExportView, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(exportID) == "" {
		return ExportView{}, errs.New(errs.CodeValidationFailed, "导出任务标识不能为空")
	}
	job, err := s.deps.Jobs.GetByID(ctx, userID, exportID)
	if err != nil {
		return ExportView{}, err
	}
	if job.State != entity.ExportStateSucceeded || job.ObjectKey == "" {
		return toView(job, ""), nil
	}

	downloadURL, expiresAt, err := s.deps.Objects.PresignGet(ctx, job.ObjectKey, entity.ExportDownloadTTL)
	if err != nil {
		return ExportView{}, errs.New(errs.CodeInternal, "签发下载地址失败").WithCause(err)
	}
	job.ExpiresAt = &expiresAt
	if err := s.deps.Jobs.Update(ctx, job); err != nil {
		return ExportView{}, err
	}
	return toView(job, downloadURL), nil
}
