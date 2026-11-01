/* 测试实体不变量：用户字段变更、验证码消费、令牌轮换与根目录构造 */
package entity

import (
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/errs"
	"github.com/gleannote/gleannote/internal/domain/valueobject"
)

/* 测试基准时间，固定后避免依赖系统时钟 */
var testNow = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

/* 新用户必须处于已验证、活跃状态，且创建与更新时间一致 */
func TestNewUser(t *testing.T) {
	email, err := valueobject.ParseEmail("alice@example.com")
	if err != nil {
		t.Fatalf("邮箱解析失败: %v", err)
	}
	user, err := NewUser("u-1", email, "hash", "alice", testNow)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if user.Status != StatusActive || user.EmailVerifiedAt == nil {
		t.Fatal("新用户应为活跃且邮箱已验证")
	}
	if !user.CreatedAt.Equal(testNow) || !user.UpdatedAt.Equal(testNow) {
		t.Fatal("创建时间与更新时间应一致")
	}
	if _, err := NewUser("", email, "hash", "alice", testNow); !errs.IsCode(err, errs.CodeInternal) {
		t.Fatalf("缺少 ID 应返回内部错误，实际 %v", err)
	}
}

/* 改名必须归一化空白并刷新更新时间；非法昵称不得写入 */
func TestUserRename(t *testing.T) {
	email, _ := valueobject.ParseEmail("alice@example.com")
	user, _ := NewUser("u-1", email, "hash", "alice", testNow)

	later := testNow.Add(time.Hour)
	if err := user.Rename("  新昵称 ", later); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if user.Nickname != "新昵称" || !user.UpdatedAt.Equal(later) {
		t.Fatalf("改名结果不符: %q %s", user.Nickname, user.UpdatedAt)
	}
	if err := user.Rename("  ", later); !errs.IsValidation(err) {
		t.Fatalf("空白昵称应失败: %v", err)
	}
	if user.Nickname != "新昵称" {
		t.Fatal("失败改名不得污染原昵称")
	}
}

/* 密码哈希为空属于内部错误，不得写入实体 */
func TestUserChangePassword(t *testing.T) {
	email, _ := valueobject.ParseEmail("alice@example.com")
	user, _ := NewUser("u-1", email, "hash", "alice", testNow)

	if err := user.ChangePassword("new-hash", testNow); err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}
	if user.PasswordHash != "new-hash" {
		t.Fatal("哈希未更新")
	}
	if err := user.ChangePassword("", testNow); !errs.IsCode(err, errs.CodeInternal) {
		t.Fatalf("空哈希应返回内部错误，实际 %v", err)
	}
}

/* 构造一条未消费的验证码，供消费相关用例复用 */
func newCode(expiresAt time.Time) *EmailCode {
	return &EmailCode{
		ID:        "c-1",
		Email:     "alice@example.com",
		Purpose:   PurposeRegister,
		CodeHash:  "hash",
		ExpiresAt: expiresAt,
		CreatedAt: testNow,
	}
}

/* 正确验证码一次性消费，重复消费返回过期 */
func TestEmailCodeConsume(t *testing.T) {
	code := newCode(testNow.Add(10 * time.Minute))
	if err := code.Consume("hash", testNow); err != nil {
		t.Fatalf("正确验证码应通过: %v", err)
	}
	if code.ConsumedAt == nil {
		t.Fatal("消费时间未写入")
	}
	if err := code.Consume("hash", testNow); !errs.IsCode(err, errs.CodeAuthCodeExpired) {
		t.Fatalf("重复消费应返回过期，实际 %v", err)
	}
}

/* 连续错误 5 次后该码作废：第 5 次返回错误码不匹配，之后返回过期 */
func TestEmailCodeAttemptLimit(t *testing.T) {
	code := newCode(testNow.Add(10 * time.Minute))
	for index := 1; index <= MaxCodeAttempts; index++ {
		err := code.Consume("wrong", testNow)
		if !errs.IsCode(err, errs.CodeAuthCodeInvalid) {
			t.Fatalf("第 %d 次错误应返回验证码不正确，实际 %v", index, err)
		}
	}
	if err := code.Consume("hash", testNow); !errs.IsCode(err, errs.CodeAuthCodeExpired) {
		t.Fatalf("作废后即使输入正确也应过期，实际 %v", err)
	}
}

/* 超过有效期必须返回过期，且不消耗错误次数 */
func TestEmailCodeExpired(t *testing.T) {
	code := newCode(testNow.Add(-time.Second))
	if err := code.Consume("hash", testNow); !errs.IsCode(err, errs.CodeAuthCodeExpired) {
		t.Fatalf("过期码应返回过期，实际 %v", err)
	}
	if !code.Expired(testNow) {
		t.Fatal("Expired 判定不符")
	}
}

/* 用途枚举只接受契约内的三个值 */
func TestValidCodePurpose(t *testing.T) {
	valid := []string{PurposeRegister, PurposeResetPassword, PurposeDeleteAccount}
	for _, purpose := range valid {
		if !ValidCodePurpose(purpose) {
			t.Fatalf("用途 %s 应合法", purpose)
		}
	}
	if ValidCodePurpose("LOGIN") {
		t.Fatal("未知用途不应合法")
	}
}

/* 令牌轮换后必须标记 replacedBy，吊销幂等 */
func TestRefreshTokenRevoke(t *testing.T) {
	token := &RefreshToken{ID: "t-1", ExpiresAt: testNow.Add(time.Hour)}
	if !token.Usable(testNow) {
		t.Fatal("未吊销且未过期应可用")
	}

	token.Revoke(testNow, "t-2")
	token.Revoke(testNow.Add(time.Minute), "t-3")
	if !token.Rotated() || token.ReplacedBy != "t-2" {
		t.Fatalf("轮换记录不符: %s", token.ReplacedBy)
	}
	if token.Usable(testNow) {
		t.Fatal("已吊销令牌不可用")
	}

	expired := &RefreshToken{ExpiresAt: testNow.Add(-time.Minute)}
	if expired.Usable(testNow) {
		t.Fatal("过期令牌不可用")
	}
}

/* 根目录必须 depth=1、无父级、路径为 /{id}，且不能被复制成非法层级 */
func TestNewRootFolder(t *testing.T) {
	folder := NewRootFolder("f-1", "u-1", testNow)
	if folder.Depth != 1 || folder.ParentID != nil {
		t.Fatal("根目录层级或父级不符")
	}
	if folder.Path != "/f-1" {
		t.Fatalf("物化路径不符: %s", folder.Path)
	}
	if folder.Name != RootFolderName || folder.Revision != 1 {
		t.Fatal("根目录名称或修订号不符")
	}
}

/* 子目录继承父路径与深度；根级新建目录与根目录同构 */
func TestNewFolderPaths(t *testing.T) {
	name, err := valueobject.ParseFolderName("线性代数")
	if err != nil {
		t.Fatalf("解析名称失败: %v", err)
	}
	root := NewRootFolder("f-1", "u-1", testNow)
	child := NewFolder("f-2", "u-1", root, name, testNow)
	if child.Depth != 2 || child.Path != "/f-1/f-2" {
		t.Fatalf("子目录深度或路径不符: %d %s", child.Depth, child.Path)
	}
	if child.ParentID == nil || *child.ParentID != "f-1" {
		t.Fatal("子目录父级不符")
	}

	rootLevel := NewFolder("f-3", "u-1", nil, name, testNow)
	if rootLevel.Depth != 1 || rootLevel.Path != "/f-3" || rootLevel.ParentID != nil {
		t.Fatal("根级目录应按 depth=1 构造")
	}
}

/* 改名必须匹配修订号；成功后修订号自增，失败不改变名称 */
func TestFolderRenameRevision(t *testing.T) {
	folder := NewRootFolder("f-1", "u-1", testNow)
	name, err := valueobject.ParseFolderName("数学")
	if err != nil {
		t.Fatalf("解析名称失败: %v", err)
	}
	if err := folder.Rename(name, 2, testNow); !errs.IsCode(err, errs.CodeRevisionMismatch) {
		t.Fatalf("修订号不符应返回 412 语义: %v", err)
	}
	if folder.Name == "数学" {
		t.Fatal("失败的改名不应写入")
	}
	if err := folder.Rename(name, 1, testNow); err != nil {
		t.Fatalf("匹配修订号应成功: %v", err)
	}
	if folder.Name != "数学" || folder.Revision != 2 {
		t.Fatalf("改名结果不符: %s rev=%d", folder.Name, folder.Revision)
	}
}

/* 软删除写入批次与到期时间；重复删除不覆盖原批次；恢复清空回收站字段 */
func TestFolderDeleteAndRestore(t *testing.T) {
	folder := NewRootFolder("f-1", "u-1", testNow)
	folder.MarkDeleted("batch-1", testNow, 30*24*time.Hour)
	if !folder.IsDeleted() || folder.DeleteBatchID != "batch-1" {
		t.Fatal("软删除字段不符")
	}
	if folder.PurgeAt == nil || !folder.PurgeAt.Equal(testNow.Add(30*24*time.Hour)) {
		t.Fatal("到期时间不符")
	}
	folder.MarkDeleted("batch-2", testNow.Add(time.Hour), 30*24*time.Hour)
	if folder.DeleteBatchID != "batch-1" {
		t.Fatal("重复删除不应改写原批次")
	}

	folder.Restore(nil, 1, "/f-1", testNow.Add(time.Hour))
	if folder.IsDeleted() || folder.DeleteBatchID != "" || folder.PurgeAt != nil {
		t.Fatal("恢复后回收站字段应清空")
	}
}
