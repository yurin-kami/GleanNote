/* 分享端点：状态查询、开启、关闭与匿名公开读取 */
package httptransport

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/gin-gonic/gin"

	shareusecase "github.com/gleannote/gleannote/internal/application/usecase/share"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/*
 * publicShareTemplate 是公开分享页的服务端模板。
 * 正文以纯文本在 <pre> 中转义输出，不解析 Markdown 与任何脚本；图片只允许页面原图地址。
 */
var publicShareTemplate = template.Must(template.New("public-share").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>{{.Title}}</title>
<style>
body{margin:0;padding:24px;background:#fafafa;color:#1f2328;font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif}
main{max-width:760px;margin:0 auto;background:#fff;border-radius:12px;padding:32px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
h1{font-size:24px;line-height:1.3;margin:0 0 16px}
pre{white-space:pre-wrap;word-break:break-word;font:inherit;line-height:1.7;margin:0}
section{margin-top:24px}
section img{max-width:100%;display:block;margin:12px 0;border-radius:8px}
footer{margin-top:24px;font-size:12px;color:#6e7781}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<pre>{{.Content}}</pre>
{{if .Pages}}<section>
{{range .Pages}}<img src="{{.OriginalURL}}" alt="第 {{.Index}} 页" loading="lazy">
{{end}}</section>{{end}}
<footer>由拾页笔记分享 · 更新于 {{.UpdatedAt}}</footer>
</main>
</body>
</html>
`))

/* publicSharePageData 是公开页模板的数据结构 */
type publicSharePageData struct {
	Title     string
	Content   string
	Pages     []publicSharePageDTO
	UpdatedAt string
}

/* shareLinkDTO 对应 openapi 的 ShareLink；未开启时只输出 enabled=false */
type shareLinkDTO struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

/* publicSharePageDTO 是公开页单页原图地址 */
type publicSharePageDTO struct {
	Index       int    `json:"index"`
	OriginalURL string `json:"originalUrl"`
}

/* publicShareDTO 是匿名读取的只读内容 */
type publicShareDTO struct {
	Title     string               `json:"title"`
	Content   string               `json:"content"`
	Pages     []publicSharePageDTO `json:"pages,omitempty"`
	UpdatedAt string               `json:"updatedAt"`
}

/* toShareLinkDTO 映射分享状态；未开启时不输出时间和地址 */
func toShareLinkDTO(view shareusecase.ShareLinkView) shareLinkDTO {
	dto := shareLinkDTO{Enabled: view.Enabled, URL: view.URL}
	if !view.CreatedAt.IsZero() {
		dto.CreatedAt = rfc3339(view.CreatedAt)
	}
	if !view.UpdatedAt.IsZero() {
		dto.UpdatedAt = rfc3339(view.UpdatedAt)
	}
	return dto
}

/* getShareLink 处理 GET /v1/notes/{noteId}/shareLink */
func (h *handler) getShareLink(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.shares.GetShareLink(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusOK, toShareLinkDTO(view))
}

/* enableShare 处理 POST /v1/notes/{noteId}/shareLink，成功返回 201 */
func (h *handler) enableShare(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	view, err := h.shares.EnableShare(c.Request.Context(), userID, c.Param("noteId"))
	if err != nil {
		respondError(c, err)
		return
	}
	respondJSON(c, http.StatusCreated, toShareLinkDTO(view))
}

/* disableShare 处理 DELETE /v1/notes/{noteId}/shareLink，成功返回 204 */
func (h *handler) disableShare(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		unauthorized(c)
		return
	}
	if err := h.shares.DisableShare(c.Request.Context(), userID, c.Param("noteId")); err != nil {
		respondError(c, err)
		return
	}
	respondNoContent(c)
}

/* publicShare 处理无鉴权的 GET /v1/public/shares/{token} */
func (h *handler) publicShare(c *gin.Context) {
	/* 撤销必须立即生效（API.md 5.9）：禁止中间缓存启发式缓存响应，410 也不得被缓存 */
	c.Header("Cache-Control", "no-store")
	view, err := h.shares.PublicNote(c.Request.Context(), c.Param("token"))
	if err != nil {
		respondError(c, err)
		return
	}
	pages := make([]publicSharePageDTO, 0, len(view.Pages))
	for _, page := range view.Pages {
		pages = append(pages, publicSharePageDTO{Index: page.Index, OriginalURL: page.OriginalURL})
	}
	respondJSON(c, http.StatusOK, publicShareDTO{
		Title:     view.Title,
		Content:   view.Content,
		Pages:     pages,
		UpdatedAt: rfc3339(view.UpdatedAt),
	})
}

/*
 * publicSharePage 处理无鉴权的 GET /s/{token}，与分享地址 {base}/s/{token} 保持一致。
 * 输出服务端渲染的只读页面；失败时仍返回统一的 JSON 错误体，便于脚本与前端处理。
 */
func (h *handler) publicSharePage(c *gin.Context) {
	/* 与 JSON 端点一致：分享关闭或被撤销后 410 需立即生效，禁止任何中间缓存 */
	c.Header("Cache-Control", "no-store")
	view, err := h.shares.PublicNote(c.Request.Context(), c.Param("token"))
	if err != nil {
		respondError(c, err)
		return
	}
	pages := make([]publicSharePageDTO, 0, len(view.Pages))
	for _, page := range view.Pages {
		pages = append(pages, publicSharePageDTO{Index: page.Index, OriginalURL: page.OriginalURL})
	}
	var buffer bytes.Buffer
	if err := publicShareTemplate.Execute(&buffer, publicSharePageData{
		Title:     view.Title,
		Content:   view.Content,
		Pages:     pages,
		UpdatedAt: rfc3339(view.UpdatedAt),
	}); err != nil {
		respondError(c, errs.New(errs.CodeInternal, "渲染公开分享页失败").WithCause(err))
		return
	}
	/* 公开页是唯一非 JSON 输出：允许页面原图与内联样式，仍禁止脚本、框架与外链资源 */
	c.Header("Content-Security-Policy",
		"default-src 'none'; img-src https: http: data:; style-src 'unsafe-inline'")
	c.Data(http.StatusOK, "text/html; charset=utf-8", buffer.Bytes())
}
