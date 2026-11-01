/*
 * 转换任务实体：异步识图的状态机、进度、失败码与额度计费信息。
 *
 * 职责：约束 QUEUED→RUNNING→终态迁移，维护进度与失败文案，禁止非法取消。
 * 不负责：任务排队、调用供应商、写入笔记正文与额度账本（由用例编排）。
 * 依赖：标准库、domain/errs。
 * 参考：docs/API.md#551-任务对象、docs/ARCHITECTURE.md#62-图片上传到转换完成
 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* JobKind 区分新建转换与追加页面；P1 的追加能力复用同一结构 */
type JobKind string

/* 任务类型取值与 docs/API.md#32-枚举字典 一致 */
const (
	JobKindCreate JobKind = "CREATE"
	JobKindAppend JobKind = "APPEND"
)

/* JobState 是任务状态，终态为 SUCCEEDED、FAILED、CANCELED */
type JobState string

/* 任务状态取值与 PRD 3.3 一致 */
const (
	JobStateQueued    JobState = "QUEUED"
	JobStateRunning   JobState = "RUNNING"
	JobStateSucceeded JobState = "SUCCEEDED"
	JobStateFailed    JobState = "FAILED"
	JobStateCanceled  JobState = "CANCELED"
)

/* JobPhase 是进度页展示阶段，独立于状态 */
type JobPhase string

/* 阶段取值：排队、识别、生成 Markdown、完成 */
const (
	JobPhaseQueued      JobPhase = "QUEUED"
	JobPhaseRecognizing JobPhase = "RECOGNIZING"
	JobPhaseGenerating  JobPhase = "GENERATING"
	JobPhaseCompleted   JobPhase = "COMPLETED"
)

/* JobFailureCode 是任务失败码，取值见 docs/API.md#32-枚举字典 */
type JobFailureCode string

/* 失败码与 PRD FR-05 的失败类型一一对应 */
const (
	JobFailureProviderUnavailable JobFailureCode = "PROVIDER_UNAVAILABLE"
	JobFailureImageUnreadable     JobFailureCode = "IMAGE_UNREADABLE"
	JobFailureContentUnsupported  JobFailureCode = "CONTENT_UNSUPPORTED"
	JobFailureTimeout             JobFailureCode = "TIMEOUT"
	JobFailureEmptyResult         JobFailureCode = "EMPTY_RESULT"
	JobFailureQuotaExceeded       JobFailureCode = "QUOTA_EXCEEDED"
	JobFailureStorageExceeded     JobFailureCode = "STORAGE_EXCEEDED"
	/* NOTE_MODIFIED 表示转换期间用户修改了笔记，结算主动放弃写入，避免覆盖用户内容 */
	JobFailureNoteModified JobFailureCode = "NOTE_MODIFIED"
	/* INTERNAL_ERROR 表示结算遇到数据库等基础设施故障，与内容本身无关 */
	JobFailureInternal JobFailureCode = "INTERNAL_ERROR"
)

/*
 * ConversionJob 是一次异步转换任务。
 * 技术字段：Attempt 记录重试次数，QuotaCharged 用于取消/失败时等额返还。
 */
type ConversionJob struct {
	ID                 string
	NoteID             string
	UserID             string
	Kind               JobKind
	State              JobState
	Phase              JobPhase
	TotalPages         int
	ProcessedPages     int
	FailureCode        JobFailureCode
	FailureUserMessage string
	QuotaCharged       int
	Attempt            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

/* NewConversionJob 创建排队中的任务；attempt 从 1 开始，重试时递增 */
func NewConversionJob(id, noteID, userID string, kind JobKind,
	totalPages, quotaCharged, attempt int, now time.Time) *ConversionJob {
	return &ConversionJob{
		ID:           id,
		NoteID:       noteID,
		UserID:       userID,
		Kind:         kind,
		State:        JobStateQueued,
		Phase:        JobPhaseQueued,
		TotalPages:   totalPages,
		QuotaCharged: quotaCharged,
		Attempt:      attempt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

/* IsTerminal 判断任务是否已结束 */
func (j *ConversionJob) IsTerminal() bool {
	switch j.State {
	case JobStateSucceeded, JobStateFailed, JobStateCanceled:
		return true
	default:
		return false
	}
}

/* CanCancel 仅排队中与处理中允许取消，口径见 docs/API.md#552 */
func (j *ConversionJob) CanCancel() bool {
	return j.State == JobStateQueued || j.State == JobStateRunning
}

/* Start 把任务置为处理中并记录开始时间；非排队状态忽略，避免覆盖终态 */
func (j *ConversionJob) Start(now time.Time) {
	if j.State != JobStateQueued {
		return
	}
	started := now
	j.State = JobStateRunning
	j.Phase = JobPhaseRecognizing
	j.StartedAt = &started
	j.UpdatedAt = now
}

/* Advance 更新已处理页数与展示阶段；仅处理中任务可推进进度 */
func (j *ConversionJob) Advance(processed int, phase JobPhase, now time.Time) {
	if j.State != JobStateRunning {
		return
	}
	if processed < 0 {
		processed = 0
	}
	if processed > j.TotalPages {
		processed = j.TotalPages
	}
	j.ProcessedPages = processed
	j.Phase = phase
	j.UpdatedAt = now
}

/* Succeed 置为成功并补齐进度；仅处理中的任务可成功 */
func (j *ConversionJob) Succeed(now time.Time) {
	if j.State != JobStateRunning {
		return
	}
	finished := now
	j.State = JobStateSucceeded
	j.Phase = JobPhaseCompleted
	j.ProcessedPages = j.TotalPages
	j.FinishedAt = &finished
	j.UpdatedAt = now
}

/*
 * Fail 置为失败并记录失败码与用户文案。
 * 允许从排队或处理中进入失败；空结果、超时与供应商错误都走此路径。
 */
func (j *ConversionJob) Fail(code JobFailureCode, now time.Time) {
	if j.IsTerminal() {
		return
	}
	finished := now
	j.State = JobStateFailed
	j.FailureCode = code
	j.FailureUserMessage = FailureMessage(code)
	j.FinishedAt = &finished
	j.UpdatedAt = now
}

/*
 * Requeue 把处理中的任务回落为排队，供进程停机时交给恢复逻辑重新投递。
 * 约束：仅处理中任务可回落；回退不退款，额度仍随任务保留。
 */
func (j *ConversionJob) Requeue(now time.Time) {
	if j.State != JobStateRunning {
		return
	}
	j.State = JobStateQueued
	j.Phase = JobPhaseQueued
	j.StartedAt = nil
	j.UpdatedAt = now
}

/* Cancel 取消排队中或处理中的任务；其他状态返回 CONVERSION_STATE_INVALID */
func (j *ConversionJob) Cancel(now time.Time) error {
	if !j.CanCancel() {
		return errs.New(errs.CodeConversionStateInvalid, "当前任务状态不可取消")
	}
	finished := now
	j.State = JobStateCanceled
	j.FinishedAt = &finished
	j.UpdatedAt = now
	return nil
}

/* FailureMessage 返回失败码对应的用户文案，与 docs/API.md#63-上传与转换 一致 */
func FailureMessage(code JobFailureCode) string {
	switch code {
	case JobFailureProviderUnavailable:
		return "当前使用人数较多，请稍后重试"
	case JobFailureImageUnreadable:
		return "部分页面不清晰，建议重拍后重试"
	case JobFailureContentUnsupported:
		return "该内容暂不支持转换"
	case JobFailureTimeout:
		return "转换超时，请重试"
	case JobFailureEmptyResult:
		return "未能识别到可用的文字内容"
	case JobFailureQuotaExceeded:
		return "今日额度不足，请减少图片后重试"
	case JobFailureStorageExceeded:
		return "云端空间不足，请清理后重试"
	case JobFailureNoteModified:
		return "笔记在转换期间被修改，请重试"
	case JobFailureInternal:
		return "转换失败，请稍后重试"
	default:
		return ""
	}
}
