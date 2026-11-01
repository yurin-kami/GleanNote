/* 认证用例的输入与输出类型；HTTP DTO 由传输层独立定义，避免协议耦合进用例 */
package auth

import "time"

/* SendCodeInput 是发送验证码的入参；Email 由用例归一化 */
type SendCodeInput struct {
	Email   string
	Purpose string
}

/* Agreements 是注册时要求同时同意的两项协议 */
type Agreements struct {
	UserAgreement bool
	PrivacyPolicy bool
}

/* RegisterInput 是注册入参；Nickname 为空时由邮箱前缀派生 */
type RegisterInput struct {
	Email      string
	Code       string
	Password   string
	Nickname   string
	DeviceID   string
	Agreements Agreements
}

/* LoginInput 是登录入参；DeviceID 为空时按 IP 维度管理登录态，ClientIP 用于绑定失败计数 */
type LoginInput struct {
	Email    string
	Password string
	DeviceID string
	ClientIP string
}

/* RefreshInput 以刷新令牌换取新的令牌对 */
type RefreshInput struct {
	RefreshToken string
	DeviceID     string
}

/* LogoutInput 携带当前访问令牌上下文与可选的刷新令牌 */
type LogoutInput struct {
	UserID          string
	TokenID         string
	AccessExpiresAt time.Time
	RefreshToken    string
	AllDevices      bool
}

/* ResetPasswordInput 通过邮箱验证码重置密码；ClientIP 用于清理对应来源的失败计数 */
type ResetPasswordInput struct {
	Email       string
	Code        string
	NewPassword string
	ClientIP    string
}

/* DeleteAccountInput 注销账号；Confirm 必须为 true 才执行 */
type DeleteAccountInput struct {
	UserID  string
	Code    string
	Confirm bool
}

/* UserView 是账号对外的稳定视图，不含密码与内部状态字段 */
type UserView struct {
	ID            string
	Email         string
	Nickname      string
	EmailVerified bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

/* Session 是登录态：双令牌与账号资料，ExpiresIn 单位为秒 */
type Session struct {
	User         UserView
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	TokenType    string
}
