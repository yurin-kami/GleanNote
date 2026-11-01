/* 分享链接实体：单篇笔记的公开只读入口 */
package entity

import "time"

/*
 * ShareLink 是一篇笔记的分享状态。
 * 约束：每篇笔记最多一条记录；Token 仅在 Enabled 时有效，关闭或删除笔记后立即失效。
 * 参考：docs/API.md#59-分享、PRD FR-15
 */
type ShareLink struct {
	UserID    string
	NoteID    string
	Token     string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

/* NewShareLink 以 CSPRNG 令牌开启分享 */
func NewShareLink(userID, noteID, token string, now time.Time) *ShareLink {
	return &ShareLink{
		UserID:    userID,
		NoteID:    noteID,
		Token:     token,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

/* Disable 关闭分享并清空令牌，旧地址立即失效 */
func (s *ShareLink) Disable(now time.Time) {
	s.Enabled = false
	s.Token = ""
	s.UpdatedAt = now
}

/* Reopen 以新令牌重新开启分享，保留首次创建时间 */
func (s *ShareLink) Reopen(token string, now time.Time) {
	s.Token = token
	s.Enabled = true
	s.UpdatedAt = now
}
