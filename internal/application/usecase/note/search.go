/* 笔记搜索用例：标题/正文关键词匹配、目录与标签过滤、标题命中优先排序 */
package note

import (
	"context"
	"html"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* 搜索相关口径：关键词 1–100 字符，摘要窗口以字符计（docs/API.md#5311-get-v1notessearch） */
const (
	maxSearchKeywordLength = 100
	snippetLeadRunes       = 30
	snippetTailRunes       = 60
	snippetFallbackRunes   = 120
	searchScopeBase        = "notes:search"
)

/*
 * SearchNotes 在未删除笔记中搜索关键词。
 * 顺序：校验关键词 → 解析目录子树与标签命中集合 → 解码游标 → 查询仓储 → 回填路径与摘要。
 * 排序：标题命中优先，同级按更新时间倒序（PRD FR-12）。
 * 参考：docs/API.md#5311-get-v1notessearch
 */
func (s *Service) SearchNotes(ctx context.Context, input SearchNotesInput) (SearchNotesResult, error) {
	if err := requireUser(input.UserID); err != nil {
		return SearchNotesResult{}, err
	}
	keyword := strings.TrimSpace(input.Query)
	if keyword == "" {
		return SearchNotesResult{}, errs.New(errs.CodeValidationFailed, "query 必填")
	}
	if utf8.RuneCountInString(keyword) > maxSearchKeywordLength {
		return SearchNotesResult{}, errs.New(errs.CodeValidationFailed, "query 最多 100 个字符")
	}

	folderIDs, err := s.searchFolderIDs(ctx, input.UserID, input.FolderID)
	if err != nil {
		return SearchNotesResult{}, err
	}
	tagIDs := dedupeStrings(input.TagIDs)
	var noteIDs []string
	if len(tagIDs) > 0 {
		noteIDs, err = s.deps.TagLinks.NoteIDsByTags(ctx, input.UserID, tagIDs)
		if err != nil {
			return SearchNotesResult{}, err
		}
		/* 标签命中集合为空时无需再查笔记表，直接返回空页 */
		if len(noteIDs) == 0 {
			return SearchNotesResult{Items: []SearchItem{}, TotalSize: 0}, nil
		}
	}

	size := normalizePageSize(input.PageSize)
	scope := searchScope(keyword, input.FolderID, tagIDs)
	cursor, err := s.decodeCursor(scope, input.PageToken)
	if err != nil {
		return SearchNotesResult{}, err
	}

	notes, total, err := s.deps.Notes.Search(ctx, port.NoteSearchQuery{
		UserID:    input.UserID,
		Keyword:   keyword,
		FolderIDs: folderIDs,
		NoteIDs:   noteIDs,
		Limit:     size + 1,
		After:     cursor,
	})
	if err != nil {
		return SearchNotesResult{}, err
	}

	nextToken := ""
	if len(notes) > size {
		notes = notes[:size]
		last := notes[len(notes)-1]
		token, err := s.deps.PageTokens.Encode(scope,
			port.NoteSearchSortKey(containsKeyword(last.Title, keyword), last.UpdatedAt), last.ID)
		if err != nil {
			return SearchNotesResult{}, errs.New(errs.CodeInternal, "生成分页令牌失败").WithCause(err)
		}
		nextToken = token
	}

	/* 同一目录的多条命中共享路径，路径查询按目录去重 */
	paths := make(map[string]string)
	items := make([]SearchItem, 0, len(notes))
	for _, note := range notes {
		path, cached := paths[note.FolderID]
		if !cached {
			path, err = s.folderPath(ctx, input.UserID, note.FolderID)
			if err != nil {
				return SearchNotesResult{}, err
			}
			paths[note.FolderID] = path
		}
		items = append(items, SearchItem{
			NoteID:     note.ID,
			Title:      note.DisplayTitle(),
			Snippet:    buildSnippet(note.Content, keyword),
			FolderPath: path,
			UpdatedAt:  note.UpdatedAt,
		})
	}
	return SearchNotesResult{Items: items, NextPageToken: nextToken, TotalSize: total}, nil
}

/* searchFolderIDs 解析目录过滤；未指定时返回 nil 表示不限目录 */
func (s *Service) searchFolderIDs(ctx context.Context, userID, folderID string) ([]string, error) {
	if folderID == "" {
		return nil, nil
	}
	if _, err := s.deps.Folders.GetByID(ctx, userID, folderID); err != nil {
		return nil, err
	}
	nodes, err := s.deps.Folders.ListSubtree(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids, nil
}

/* searchScope 把关键词、目录与标签编入令牌作用域，禁止跨查询复用 */
func searchScope(keyword, folderID string, tagIDs []string) string {
	sorted := append([]string(nil), tagIDs...)
	sort.Strings(sorted)
	return searchScopeBase + ":" + keyword + ":" + folderID + ":" + strings.Join(sorted, ",")
}

/* containsKeyword 判断文本是否包含关键词，忽略大小写 */
func containsKeyword(text, keyword string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(keyword))
}

/*
 * buildSnippet 生成带 <em> 高亮的摘要。
 * 窗口以命中位置为中心截取；正文未命中（仅标题命中）时返回正文开头。
 * 除 <em> 外全部转义，避免把正文中的标记透出给客户端。
 */
func buildSnippet(content, keyword string) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return ""
	}
	lowered := lowerRunes(runes)
	needle := lowerRunes([]rune(keyword))
	match := indexRunes(lowered, needle)
	if match < 0 {
		if len(runes) > snippetFallbackRunes {
			return html.EscapeString(string(runes[:snippetFallbackRunes])) + "…"
		}
		return html.EscapeString(string(runes))
	}

	start := match - snippetLeadRunes
	if start < 0 {
		start = 0
	}
	end := match + len(needle) + snippetTailRunes
	if end > len(runes) {
		end = len(runes)
	}
	var builder strings.Builder
	if start > 0 {
		builder.WriteString("…")
	}
	builder.WriteString(html.EscapeString(string(runes[start:match])))
	builder.WriteString("<em>")
	builder.WriteString(html.EscapeString(string(runes[match : match+len(needle)])))
	builder.WriteString("</em>")
	builder.WriteString(html.EscapeString(string(runes[match+len(needle) : end])))
	if end < len(runes) {
		builder.WriteString("…")
	}
	return builder.String()
}

/* lowerRunes 逐字符小写化，保持与原文一一对应的长度 */
func lowerRunes(runes []rune) []rune {
	lowered := make([]rune, len(runes))
	for index, char := range runes {
		lowered[index] = unicode.ToLower(char)
	}
	return lowered
}

/* indexRunes 返回 needle 在 haystack 中首次出现的字符下标，未命中返回 -1 */
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for start := 0; start <= len(haystack)-len(needle); start++ {
		matched := true
		for offset, char := range needle {
			if haystack[start+offset] != char {
				matched = false
				break
			}
		}
		if matched {
			return start
		}
	}
	return -1
}

/* dedupeStrings 按首次出现顺序去重，保持过滤条件稳定 */
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
