/*
 * 额度与容量值对象：日额度、云端容量与页面数量上限的唯一口径。
 *
 * 职责：集中声明产品限额，供用例校验与响应展示共用，避免各处复制魔法数字。
 * 不负责：账本流水、容量累计与并发控制（见 port.QuotaLedger / port.StorageUsage）。
 * 依赖：仅标准库。
 * 参考：docs/PRD-拾页笔记-GleanNote.md#53-额度规则、docs/PRD-拾页笔记-GleanNote.md#531-云端存储规则
 */
package valueobject

/* 产品限额：日额度 50 张、容量 200MB、单笔记 50 页、单次提交 20 张 */
const (
	DailyPageQuota          = 50
	StorageLimitBytes int64 = 200 * 1024 * 1024
	StorageWarnPct          = 90
	MaxNotePages            = 50
	MaxUploadBatch          = 20
)

/*
 * UsedPercent 把已用容量换算为百分比，向上取整。
 * 用途：额度接口与创建响应展示；达 90% 只提示，达 100% 阻断新提交。
 */
func UsedPercent(usedBytes, limitBytes int64) int {
	if limitBytes <= 0 || usedBytes <= 0 {
		return 0
	}
	percent := int((usedBytes*100 + limitBytes - 1) / limitBytes)
	if percent > 100 {
		return 100
	}
	return percent
}
