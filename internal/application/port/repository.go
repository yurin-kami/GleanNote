/*
 * 认证模块所需的持久化端口。
 *
 * 职责：以领域实体为边界描述读写契约，隐藏 SQL、索引与事务实现。
 * 不负责：业务判断；所有按 ID 查询必须显式携带 userID（防越权，见 G-08）。
 * 依赖：domain/entity、domain/errs；实现位于 infrastructure。
 * 参考：docs/ARCHITECTURE.md#55-数据模型-postgresql-16、docs/CONVENTIONS.md#31-通用 G-08
 */

package port

import (
	"context"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
)

/*
 * UserRepository 管理账号行。
 * 约定：邮箱唯一冲突返回 AUTH_EMAIL_ALREADY_EXISTS；未找到返回 RESOURCE_NOT_FOUND。
 */
type UserRepository interface {
	Create(ctx context.Context, user *entity.User) error
	GetByEmail(ctx context.Context, email string) (*entity.User, error)
	GetByID(ctx context.Context, userID string) (*entity.User, error)
	Update(ctx context.Context, user *entity.User) error
	Delete(ctx context.Context, userID string) error
}

/*
 * EmailCodeRepository 管理验证码发送记录。
 * LatestActive 返回同邮箱同用途下最近一条未消费记录，不存在时返回 RESOURCE_NOT_FOUND。
 */
type EmailCodeRepository interface {
	Create(ctx context.Context, code *entity.EmailCode) error
	LatestActive(ctx context.Context, email, purpose string) (*entity.EmailCode, error)
	Update(ctx context.Context, code *entity.EmailCode) error
	InvalidateActive(ctx context.Context, email, purpose string, now time.Time) error
}

/*
 * RefreshTokenRepository 按哈希检索令牌，批量吊销以用户或设备为维度。
 * 约定：GetForUpdate 必须在事务内调用并锁定令牌行，供刷新轮换串行化；
 * Update 仅当 revoked_at IS NULL 时生效并返回 true，false 表示已被并发轮换或吊销。
 */
type RefreshTokenRepository interface {
	Create(ctx context.Context, token *entity.RefreshToken) error
	GetByHash(ctx context.Context, tokenHash string) (*entity.RefreshToken, error)
	GetForUpdate(ctx context.Context, tokenHash string) (*entity.RefreshToken, error)
	Update(ctx context.Context, token *entity.RefreshToken) (bool, error)
	RevokeAll(ctx context.Context, userID string, now time.Time) error
	RevokeDevice(ctx context.Context, userID, deviceID string, now time.Time) error
}

/*
 * FolderWriter 是账号模块需要的目录写入子集。
 * 注册时创建根目录；注销时的目录清理由 AccountDataPurger 统一负责。
 * 参考：docs/ARCHITECTURE.md#86-认证与授权、PRD FR-01
 */
type FolderWriter interface {
	Create(ctx context.Context, folder *entity.Folder) error
}

/*
 * AccountDataPurger 清除账号的全部业务数据，供注销时满足隐私删除承诺。
 * 约束：调用方必须在同一事务内执行；实现按 user_id 覆盖笔记、页面、版本、分享、
 * 标签、转换任务、额度账本、容量统计、上传会话、导入导出与变更日志等派生数据。
 * 账号行本身由调用方在清理后删除，因此实现可据此定位邮箱类数据。
 * 参考：docs/ARCHITECTURE.md#812-安全与隐私
 */
type AccountDataPurger interface {
	PurgeForUser(ctx context.Context, userID string) error
}

/*
 * Transactor 是事务边界端口。
 * 约束：传入的 fn 必须在同一事务上下文内执行，事务只包裹数据库操作，禁止网络调用（G-07）。
 */
type Transactor interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

/*
 * HealthChecker 是依赖连通性端口，供就绪探针追加检查项。
 * 约定：Name 返回用于探针字段的稳定名称（如 postgres、redis）；未装配时探针不追加该项。
 */
type HealthChecker interface {
	Name() string
	Ping(ctx context.Context) error
}
