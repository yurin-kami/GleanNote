/* 用户实体：账号资料与凭证变更的唯一入口 */
package entity

import (
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* StatusActive 是唯一可登录的账号状态；注销走物理删除而非状态位 */
const StatusActive = "ACTIVE"

/*
 * User 是笔记所有者。
 * PasswordHash 只保存端口产出的编码串，实体永不接触明文密码。
 */
type User struct {
	ID              string
	Email           string
	PasswordHash    string
	Nickname        string
	Status          string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

/*
 * NewUser 创建已完成邮箱验证的账号。
 * 约束：ID、邮箱、密码哈希、昵称必须已由上层归一化；任一缺失视为内部错误。
 */
func NewUser(id string, email valueobject.Email, passwordHash, nickname string, now time.Time) (*User, error) {
	if id == "" || passwordHash == "" || email.String() == "" || nickname == "" {
		return nil, errs.New(errs.CodeInternal, "创建用户缺少必要字段")
	}
	verified := now
	return &User{
		ID:              id,
		Email:           email.String(),
		PasswordHash:    passwordHash,
		Nickname:        nickname,
		Status:          StatusActive,
		EmailVerifiedAt: &verified,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

/*
 * Rename 修改昵称并刷新更新时间。
 * 约束：昵称在此处再次归一化，确保任何调用路径都无法写入非法昵称。
 */
func (u *User) Rename(nickname string, now time.Time) error {
	normalized, err := valueobject.NormalizeNickname(nickname)
	if err != nil {
		return err
	}
	u.Nickname = normalized
	u.UpdatedAt = now
	return nil
}

/*
 * ChangePassword 替换密码哈希。
 * 约束：哈希必须由 application 层的 PasswordHasher 生成，实体只做非空校验。
 */
func (u *User) ChangePassword(hash string, now time.Time) error {
	if hash == "" {
		return errs.New(errs.CodeInternal, "密码哈希不能为空")
	}
	u.PasswordHash = hash
	u.UpdatedAt = now
	return nil
}

/* Deleted 表示账号已被物理删除标记，仓储据此拒绝返回 */
func (u *User) Deleted() bool {
	return u.DeletedAt != nil
}
