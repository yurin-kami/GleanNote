/* 测试目录路径规则：拼接、子树判定边界、前缀搬迁与面包屑展示 */
package service

import "testing"

/* 根级路径为 /{id}；父路径末尾的分隔符不得产生双斜杠 */
func TestBuildFolderPath(t *testing.T) {
	if got := BuildFolderPath("", "f-1"); got != "/f-1" {
		t.Fatalf("根级路径不符: %s", got)
	}
	if got := BuildFolderPath("/f-1", "f-2"); got != "/f-1/f-2" {
		t.Fatalf("子级路径不符: %s", got)
	}
	if got := BuildFolderPath("/f-1/", "f-2"); got != "/f-1/f-2" {
		t.Fatalf("末尾分隔符应被裁剪: %s", got)
	}
}

/* 子树判定必须按「/」对齐，/a/bc 不能被当作 /a/b 的后代 */
func TestIsWithinSubtreeBoundary(t *testing.T) {
	if !IsWithinSubtree("/a/b", "/a/b") {
		t.Fatal("自身应属于子树")
	}
	if !IsWithinSubtree("/a/b", "/a/b/c") {
		t.Fatal("后代应属于子树")
	}
	if IsWithinSubtree("/a/b", "/a/bc") {
		t.Fatal("/a/bc 不是 /a/b 的后代")
	}
	if IsWithinSubtree("/a/b", "/a") {
		t.Fatal("祖先不属于子树")
	}
	if IsWithinSubtree("", "/a") || IsWithinSubtree("/a", "") {
		t.Fatal("空路径必须返回 false")
	}
}

/* 搬迁只替换完全匹配的前缀；不匹配的节点保持原路径 */
func TestRelocatePath(t *testing.T) {
	if got := RelocatePath("/a/b/c", "/a/b", "/x/y"); got != "/x/y/c" {
		t.Fatalf("后代路径不符: %s", got)
	}
	if got := RelocatePath("/a/b", "/a/b", "/x/y"); got != "/x/y" {
		t.Fatalf("根路径不符: %s", got)
	}
	if got := RelocatePath("/a/bc", "/a/b", "/x/y"); got != "/a/bc" {
		t.Fatalf("非子树节点不应改写: %s", got)
	}
}

/* 面包屑用「 / 」连接，空切片返回空串 */
func TestDisplayPath(t *testing.T) {
	if got := DisplayPath([]string{"我的笔记", "数学", "线性代数"}); got != "我的笔记 / 数学 / 线性代数" {
		t.Fatalf("展示路径不符: %s", got)
	}
	if got := DisplayPath(nil); got != "" {
		t.Fatalf("空面包屑应为空串: %q", got)
	}
}
