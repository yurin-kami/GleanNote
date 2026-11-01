/* 认证用例的共享辅助：令牌哈希、会话组装、设备标识与错误元数据 */
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/platform/random"
)

/* hashRefreshToken 只存刷新令牌的 SHA-256 摘要；原始令牌只在响应中出现一次 */
func hashRefreshToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

/* toUserView 把实体映射为对外视图，指针字段转为布尔语义 */
func toUserView(user *entity.User) UserView {
	return UserView{
		ID:            user.ID,
		Email:         user.Email,
		Nickname:      user.Nickname,
		EmailVerified: user.EmailVerifiedAt != nil,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
	}
}

/*
 * prepareRefresh 生成刷新令牌原文与待落库实体。
 * TokenHash 与 ID 在同一处生成，避免调用方误存原文。
 */
func (s *Service) prepareRefresh(userID, deviceID string, now time.Time) (string, *entity.RefreshToken, error) {
	raw, err := random.Token(32)
	if err != nil {
		return "", nil, errs.New(errs.CodeInternal, "生成刷新令牌失败").WithCause(err)
	}
	id, err := s.ids.New()
	if err != nil {
		return "", nil, errs.New(errs.CodeInternal, "生成刷新令牌 ID 失败").WithCause(err)
	}
	token := &entity.RefreshToken{
		ID:        id,
		UserID:    userID,
		TokenHash: hashRefreshToken(raw),
		DeviceID:  deviceID,
		ExpiresAt: now.Add(RefreshTokenTTL),
		CreatedAt: now,
	}
	return raw, token, nil
}

/* newSession 在事务提交后签发访问令牌，避免把外部签名放进数据库事务 */
func (s *Service) newSession(user *entity.User, refreshRaw, deviceID string, now time.Time) (Session, error) {
	tokenID, err := s.ids.New()
	if err != nil {
		return Session{}, errs.New(errs.CodeInternal, "生成访问令牌 ID 失败").WithCause(err)
	}
	access, expiresAt, err := s.deps.AccessTokens.Issue(user.ID, deviceID, tokenID, now)
	if err != nil {
		return Session{}, errs.New(errs.CodeInternal, "签发访问令牌失败").WithCause(err)
	}
	return Session{
		User:         toUserView(user),
		AccessToken:  access,
		RefreshToken: refreshRaw,
		ExpiresIn:    int(expiresAt.Sub(now).Seconds()),
		TokenType:    "Bearer",
	}, nil
}

/* normalizeDeviceID 去空白并限制长度；空值合法，表示按 IP 维度管理 */
func normalizeDeviceID(raw string) (string, error) {
	device := strings.TrimSpace(raw)
	if len(device) > maxDeviceIDLength {
		return "", errs.New(errs.CodeValidationFailed, "deviceId 超过 128 个字符")
	}
	return device, nil
}

/* validateVerificationCode 要求 6 位纯数字，避免把明显非法的输入送入哈希比较 */
func validateVerificationCode(code string) error {
	if len(code) != 6 {
		return errs.New(errs.CodeValidationFailed, "验证码必须为 6 位数字")
	}
	for index := 0; index < len(code); index++ {
		if code[index] < '0' || code[index] > '9' {
			return errs.New(errs.CodeValidationFailed, "验证码必须为 6 位数字")
		}
	}
	return nil
}

/*
 * withRetry 把剩余等待时间写入元数据。
 * 秒数向上取整且至少为 1，客户端按 metadata.retryAfterSeconds 展示。
 */
func withRetry(business *errs.Error, wait time.Duration) *errs.Error {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return business.WithMeta("retryAfterSeconds", seconds)
}

/* isNotFound 判断仓储未命中，用于把底层错误翻译为认证语义 */
func isNotFound(err error) bool {
	return errs.IsCode(err, errs.CodeResourceNotFound)
}

/* retrySeconds 把剩余时长换算为响应字段，供 429 分支复用 */
func retrySeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

/* newID 生成资源 ID，失败统一映射为内部错误 */
func (s *Service) newID(purpose string) (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成"+purpose+"ID 失败").WithCause(err)
	}
	return id, nil
}
