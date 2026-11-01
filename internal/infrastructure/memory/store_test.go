/* 测试内存存储的事务语义：提交可见、失败回滚（含新建与更新） */
package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* testUser 构造可直接落库的账号，字段只保留仓储校验所需部分 */
func testUser(id, email string) *entity.User {
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	return &entity.User{ID: id, Email: email, Nickname: "n", Status: entity.StatusActive, CreatedAt: now, UpdatedAt: now}
}

/* 事务提交后写入必须对其他操作可见 */
func TestTransactionCommit(t *testing.T) {
	store := NewStore()
	users := NewUserRepository(store)

	err := store.WithinTransaction(context.Background(), func(ctx context.Context) error {
		return users.Create(ctx, testUser("u-1", "a@example.com"))
	})
	if err != nil {
		t.Fatalf("事务提交失败: %v", err)
	}
	if _, err := users.GetByID(context.Background(), "u-1"); err != nil {
		t.Fatalf("提交后应能读到账号: %v", err)
	}
}

/* 事务失败后新建记录必须撤销，且邮箱索引一并恢复 */
func TestTransactionRollbackCreate(t *testing.T) {
	store := NewStore()
	users := NewUserRepository(store)

	err := store.WithinTransaction(context.Background(), func(ctx context.Context) error {
		if err := users.Create(ctx, testUser("u-1", "a@example.com")); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("应返回事务错误")
	}
	if _, err := users.GetByID(context.Background(), "u-1"); !errs.IsCode(err, errs.CodeResourceNotFound) {
		t.Fatalf("回滚后不应存在账号: %v", err)
	}

	/* 回滚必须释放邮箱索引，允许同一邮箱重新注册 */
	if err := users.Create(context.Background(), testUser("u-2", "a@example.com")); err != nil {
		t.Fatalf("邮箱应可复用: %v", err)
	}
}

/* 事务内更新失败后必须恢复更新前的快照 */
func TestTransactionRollbackUpdate(t *testing.T) {
	store := NewStore()
	users := NewUserRepository(store)
	ctx := context.Background()

	if err := users.Create(ctx, testUser("u-1", "a@example.com")); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	err := store.WithinTransaction(ctx, func(tx context.Context) error {
		user, err := users.GetByID(tx, "u-1")
		if err != nil {
			return err
		}
		user.Nickname = "改后"
		if err := users.Update(tx, user); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("应返回事务错误")
	}

	user, err := users.GetByID(ctx, "u-1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if user.Nickname != "n" {
		t.Fatalf("回滚后昵称应为原值，实际 %s", user.Nickname)
	}
}

/* 账号删除的邮箱索引恢复必须支持再次注册同邮箱，测试注销清理路径 */
func TestDeleteRestoresEmailIndexOnRollback(t *testing.T) {
	store := NewStore()
	users := NewUserRepository(store)
	ctx := context.Background()

	if err := users.Create(ctx, testUser("u-1", "a@example.com")); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	err := store.WithinTransaction(ctx, func(tx context.Context) error {
		if err := users.Delete(tx, "u-1"); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("应返回事务错误")
	}
	if _, err := users.GetByEmail(ctx, "a@example.com"); err != nil {
		t.Fatalf("回滚后邮箱索引应恢复: %v", err)
	}
}
