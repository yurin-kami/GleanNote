/* 版本用例的共享辅助：鉴权前置、视图映射、排序键与分页令牌 */
package version

import (
	"fmt"
	"math"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* requireUser 拒绝缺失访问令牌上下文的调用，语义与认证模块一致 */
func requireUser(userID string) error {
	if userID == "" {
		return errs.New(errs.CodeAuthTokenInvalid, "缺少访问令牌上下文")
	}
	return nil
}

/* toVersionView 映射列表项；正文只在详情接口输出 */
func toVersionView(version *entity.NoteVersion) VersionView {
	return VersionView{
		ID:        version.ID,
		Source:    string(version.Source),
		CharCount: version.CharCount,
		CreatedAt: version.CreatedAt,
		ExpiresAt: version.ExpiresAt,
	}
}

/* versionSortKey 把生成时间倒序编码为升序键，供分页令牌与排序共用 */
func versionSortKey(version *entity.NoteVersion) string {
	return fmt.Sprintf("%019d", math.MaxInt64-version.CreatedAt.UnixNano())
}

/* normalizePageSize 收敛分页大小；负数已由传输层拦截，这里兜底为默认值 */
func normalizePageSize(size int) int {
	if size <= 0 {
		return defaultPageSize
	}
	if size > maxPageSize {
		return maxPageSize
	}
	return size
}

/* versionScope 把笔记 ID 编入令牌作用域，禁止跨笔记复用游标 */
func versionScope(noteID string) string {
	return versionPageScopeBase + ":" + noteID
}

/* newID 生成版本标识；随机源异常按内部错误上抛 */
func (s *Service) newID() (string, error) {
	id, err := s.ids.New()
	if err != nil {
		return "", errs.New(errs.CodeInternal, "生成版本 ID 失败").WithCause(err)
	}
	return id, nil
}
