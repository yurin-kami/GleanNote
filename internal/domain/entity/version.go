/* 版本实体：记录正文快照的来源、字数与保留期 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* VersionSource 是版本生成来源，取值与 docs/API.md#32-枚举字典 一致 */
type VersionSource string

/*
 * 版本来源常量。
 * 只在转换完成、手动保存、自动保存、冲突副本与恢复时生成版本（PRD FR-16）。
 */
const (
	VersionSourceConversion   VersionSource = "CONVERSION"
	VersionSourceManualSave   VersionSource = "MANUAL_SAVE"
	VersionSourceAutoSave     VersionSource = "AUTO_SAVE"
	VersionSourceConflictCopy VersionSource = "CONFLICT_COPY"
	VersionSourceRestore      VersionSource = "RESTORE"
)

/*
 * 版本保留口径：每篇保留最近 50 个版本且仅保留 90 天内生成的版本（PRD FR-16）。
 * 版本正文不计入 200MB 云端容量。
 */
const (
	MaxVersionsPerNote = 50
	VersionRetention   = 90 * 24 * time.Hour

	/* AutoVersionMinInterval 是同一笔记两次自动保存版本的最小间隔（PRD FR-16） */
	AutoVersionMinInterval = 5 * time.Minute
)

/*
 * NoteVersion 是正文的历史快照。
 * 约束：只记录正文，不记录标题、标签与目录；ExpiresAt 到期后由清理逻辑删除。
 */
type NoteVersion struct {
	ID        string
	UserID    string
	NoteID    string
	Source    VersionSource
	Content   string
	CharCount int
	CreatedAt time.Time
	ExpiresAt time.Time
}

/* NewNoteVersion 构造一次正文快照；字数由内容值对象计算，到期时间按保留期推算 */
func NewNoteVersion(id, userID, noteID string, source VersionSource,
	content valueobject.NoteContent, now time.Time) *NoteVersion {
	return &NoteVersion{
		ID:        id,
		UserID:    userID,
		NoteID:    noteID,
		Source:    source,
		Content:   content.String(),
		CharCount: content.CharCount(),
		CreatedAt: now,
		ExpiresAt: now.Add(VersionRetention),
	}
}

/* Expired 判断版本是否已超出保留期 */
func (v *NoteVersion) Expired(now time.Time) bool {
	return !v.ExpiresAt.After(now)
}
