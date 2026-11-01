/* Markdown 压缩包生成：单篇与目录两种口径，图片使用包内相对路径 */
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/gleannote/gleannote/internal/application/port"
	"github.com/gleannote/gleannote/internal/domain/entity"
	"github.com/gleannote/gleannote/internal/domain/errs"
)

/* zipTimestamp 固定压缩包的修改时间，保证同一内容生成字节稳定的归档 */
var zipTimestamp = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

/* archiveFile 是压缩包内的一条记录；名字以 / 结尾表示目录 */
type archiveFile struct {
	name string
	data []byte
}

/* buildMarkdownArchive 生成 Markdown 压缩包：单篇导出或目录导出 */
func (s *Service) buildMarkdownArchive(ctx context.Context, userID, noteID, folderID string) ([]byte, error) {
	files := make([]archiveFile, 0, 16)
	var err error
	if noteID != "" {
		files, err = s.singleNoteFiles(ctx, userID, noteID)
	} else {
		files, err = s.folderFiles(ctx, userID, folderID)
	}
	if err != nil {
		return nil, err
	}
	return zipFiles(files)
}

/* singleNoteFiles 生成单篇导出：根目录一篇 Markdown 与 images/ 目录 */
func (s *Service) singleNoteFiles(ctx context.Context, userID, noteID string) ([]archiveFile, error) {
	note, err := s.loadExportableNote(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	pages, err := s.deps.Pages.ListAll(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	base := sanitizeFileName(note.DisplayTitle())
	markdown, images, err := s.buildNoteExport(ctx, note, pages, base, "images", "images")
	if err != nil {
		return nil, err
	}
	files := make([]archiveFile, 0, len(images)+1)
	files = append(files, archiveFile{name: base + ".md", data: markdown})
	return append(files, images...), nil
}

/*
 * folderFiles 生成目录导出：镜像子目录层级，空目录保留为空文件夹。
 * 每篇正常笔记一个 .md，图片按笔记分别放入对应 images 目录（PRD FR-14）。
 */
func (s *Service) folderFiles(ctx context.Context, userID, folderID string) ([]archiveFile, error) {
	root, err := s.loadActiveFolder(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.deps.Folders.ListSubtree(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}

	/* 回收站中的目录连同其子树都带删除标记，导出时整节点跳过，避免把已删除名称写进压缩包 */
	active := make([]*entity.Folder, 0, len(nodes))
	for _, node := range nodes {
		if node.IsDeleted() {
			continue
		}
		active = append(active, node)
	}
	nodes = active
	/* 按深度排序后逐层拼接路径，父级一定先于子级被处理 */
	paths := make(map[string]string, len(nodes))
	/*
	 * 目录名清洗后可能重名（如 A/B 与 A_B 都会变成 A_B），按父目录维度去重，
	 * 避免 zip 内出现同名目录条目导致解压覆盖。
	 */
	siblingNames := make(map[string]map[string]int, len(nodes))
	files := make([]archiveFile, 0, len(nodes)*2)
	for _, node := range nodes {
		parentPath := ""
		if node.ID != root.ID && node.ParentID != nil {
			parentPath = paths[*node.ParentID]
		}
		used, ok := siblingNames[parentPath]
		if !ok {
			used = make(map[string]int, 1)
			siblingNames[parentPath] = used
		}
		base := uniqueBaseName(used, sanitizeFileName(node.Name))
		relative := base
		if parentPath != "" {
			relative = parentPath + "/" + base
		}
		paths[node.ID] = relative
		files = append(files, archiveFile{name: relative + "/"})
	}

	normal := entity.NoteStateNormal
	for _, node := range nodes {
		directory := paths[node.ID]
		notes, _, err := s.deps.Notes.List(ctx, port.NoteListQuery{
			UserID:   userID,
			FolderID: node.ID,
			State:    &normal,
			Order:    port.NoteOrderTitleAsc,
		})
		if err != nil {
			return nil, err
		}
		used := make(map[string]int, len(notes))
		for _, note := range notes {
			pages, err := s.deps.Pages.ListAll(ctx, userID, note.ID)
			if err != nil {
				return nil, err
			}
			base := uniqueBaseName(used, sanitizeFileName(note.DisplayTitle()))
			markdown, images, err := s.buildNoteExport(ctx, note, pages, base,
				"images/"+base, directory+"/images/"+base)
			if err != nil {
				return nil, err
			}
			files = append(files, archiveFile{name: directory + "/" + base + ".md", data: markdown})
			files = append(files, images...)
		}
	}
	return files, nil
}

/*
 * buildNoteExport 生成一篇笔记的 Markdown 与图片文件。
 * markdownRefs 为 Markdown 中的相对目录；zipDirectory 为图片在包内的实际目录。
 */
func (s *Service) buildNoteExport(ctx context.Context, note *entity.Note, pages []*entity.NotePage,
	base, markdownRefs, zipDirectory string) ([]byte, []archiveFile, error) {
	images := make([]archiveFile, 0, len(pages))
	references := make([]string, 0, len(pages))
	for _, page := range pages {
		info, err := s.deps.Objects.Stat(ctx, page.ObjectKey)
		if err != nil {
			return nil, nil, errs.New(errs.CodeImageUnreadable, "页面图片不可用，请联网后重试").
				WithMeta("pageIndex", page.Index).WithCause(err)
		}
		extension := imageExtension(info.ContentType)
		fileName := fmt.Sprintf("page-%03d%s", page.Index, extension)
		data, err := s.deps.Objects.Get(ctx, page.ObjectKey)
		if err != nil {
			return nil, nil, errs.New(errs.CodeImageUnreadable, "页面图片不可用，请联网后重试").
				WithMeta("pageIndex", page.Index).WithCause(err)
		}
		images = append(images, archiveFile{name: zipDirectory + "/" + fileName, data: data})
		references = append(references, fmt.Sprintf("![第 %d 页](%s/%s)", page.Index, markdownRefs, fileName))
	}

	body := strings.TrimRight(note.Content, "\r\n")
	if strings.TrimSpace(body) == "" {
		body = "# " + note.DisplayTitle()
	}
	if len(references) > 0 {
		body += "\n\n## 页面图片\n\n" + strings.Join(references, "\n\n")
	}
	return []byte(body + "\n"), images, nil
}

/*
 * sanitizeFileName 按 PRD FR-14 把文件名非法字符替换为下划线。
 * 约束：去除首尾空格与点，超长时按字符截断，空结果回落到「未命名笔记」。
 */
func sanitizeFileName(name string) string {
	replaced := strings.Map(func(char rune) rune {
		switch char {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if unicode.IsControl(char) {
			return '_'
		}
		return char
	}, strings.TrimSpace(name))
	replaced = strings.Trim(replaced, " .")
	if replaced == "" {
		return "未命名笔记"
	}
	runes := []rune(replaced)
	if len(runes) > 80 {
		replaced = string(runes[:80])
	}
	return replaced
}

/* uniqueBaseName 在同一目录内为重复标题追加序号，规则为「标题 (2)」起 */
func uniqueBaseName(used map[string]int, base string) string {
	key := strings.ToLower(base)
	used[key]++
	if used[key] == 1 {
		return base
	}
	return fmt.Sprintf("%s (%d)", base, used[key])
}

/* zipFiles 把记录写入 zip；目录记录使用 Store 方式以保留空目录 */
func zipFiles(files []archiveFile) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Modified: zipTimestamp}
		if strings.HasSuffix(file.name, "/") {
			header.Method = zip.Store
		} else {
			header.Method = zip.Deflate
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, errs.New(errs.CodeInternal, "生成导出压缩包失败").WithCause(err)
		}
		if _, err := entry.Write(file.data); err != nil {
			return nil, errs.New(errs.CodeInternal, "写入导出压缩包失败").WithCause(err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, errs.New(errs.CodeInternal, "关闭导出压缩包失败").WithCause(err)
	}
	return buffer.Bytes(), nil
}
