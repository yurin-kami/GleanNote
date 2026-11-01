/* 测试业务错误类型：码值透传、元数据隔离、错误链包装与判定 */
package errs

import (
	"errors"
	"fmt"
	"testing"
)

/* 基础构造必须保留错误码与细节 */
func TestNewError(t *testing.T) {
	err := New(CodeResourceNotFound, "笔记不存在")
	if err.Code() != CodeResourceNotFound {
		t.Fatalf("错误码不符: %s", err.Code())
	}
	if err.Detail() != "笔记不存在" {
		t.Fatalf("细节不符: %s", err.Detail())
	}
	if len(err.Meta()) != 0 {
		t.Fatal("未设置元数据时应为空")
	}
}

/* Meta 读取必须返回副本，调用方修改不得影响原错误 */
func TestMetaIsolation(t *testing.T) {
	err := New(CodeQuotaExceeded, "额度不足").WithMeta("remaining", 5)
	meta := err.Meta()
	meta["remaining"] = 0
	if got := err.Meta()["remaining"]; got != 5 {
		t.Fatalf("元数据被外部修改: %v", got)
	}
}

/* 包装后仍应识别错误码，并保留底层原因 */
func TestWrappedError(t *testing.T) {
	cause := errors.New("database timeout")
	wrapped := fmt.Errorf("查询用户: %w", New(CodeInternal, "内部错误").WithCause(cause))

	if !IsCode(wrapped, CodeInternal) {
		t.Fatal("包装后应识别 INTERNAL")
	}
	if code, ok := CodeOf(wrapped); !ok || code != CodeInternal {
		t.Fatalf("CodeOf 结果不符: %s %v", code, ok)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("应保留底层错误链")
	}
}

/* 非业务错误与错误码不匹配时判断必须为假 */
func TestCodeMatching(t *testing.T) {
	plain := errors.New("plain")
	if _, ok := As(plain); ok {
		t.Fatal("普通错误不应被识别为业务错误")
	}
	if IsValidation(plain) {
		t.Fatal("普通错误不应是校验失败")
	}
	if IsValidation(New(CodeValidationFailed, "字段非法")) != true {
		t.Fatal("校验错误应被识别")
	}
}
