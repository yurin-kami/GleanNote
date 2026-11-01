/*
 * AIP-136 自定义方法路径改写：对外 /v1/auth:sendCode，内部路由为 /v1/auth/sendCode。
 *
 * 职责：在进入 Gin 路由树之前把末段冒号动词改写为子路径。
 * 原因：Gin 把路径中的 ':' 一律视为通配符起点，同一集合下多个动词会互相冲突。
 * 参考：Google AIP-136 <https://google.aip.dev/136>、docs/API.md#21-基础约定
 */
package httptransport

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

/* customMethodPattern 只匹配末段带冒号动词的路径，冒号前必须至少有一个字符 */
var customMethodPattern = regexp.MustCompile(`^(/[^:]+):([A-Za-z][A-Za-z0-9]*)$`)

/*
 * collectionCustomPrefix 承接「集合级」自定义方法。
 * Gin 在同一 HTTP 方法下不允许静态段与 :param 作为兄弟节点，
 * 因此 /notes:createFromPages 不能直接落到 /notes/createFromPages（与 /notes/:noteId 冲突），
 * 改为改写到独立前缀下注册。
 */
const collectionCustomPrefix = "/v1/_custom"

/* collectionCustomScopes 列出需要独立前缀的集合路径，仅在有同层通配段时加入 */
var collectionCustomScopes = map[string]struct{}{
	"/v1/notes": {},
}

/* rewriteCustomMethods 包装 Gin 引擎，让对外路径保持不变的同时复用内部路由表 */
func rewriteCustomMethods(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matches := customMethodPattern.FindStringSubmatch(r.URL.Path)
		if matches == nil {
			next.ServeHTTP(w, r)
			return
		}

		target := matches[1] + "/" + matches[2]
		if _, ok := collectionCustomScopes[matches[1]]; ok {
			target = collectionCustomPrefix + strings.TrimPrefix(matches[1], "/v1") + "/" + matches[2]
		}

		/* 原始路径写入上下文，problem.instance 与访问日志仍展示契约路径 */
		original := context.WithValue(r.Context(), originalPathKey{}, r.URL.Path)
		clone := r.Clone(original)
		clone.URL.Path = target
		clone.URL.RawPath = ""
		next.ServeHTTP(w, clone)
	})
}
