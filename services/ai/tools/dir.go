package aitools

// 目录工具：列出一个目录里有什么。
//
// 为什么 agent 独有而不是对外：file_search（Everything）只知道「文件名→路径」，
// 不知道层级；模型要理解一个项目，第一步必须是「这个目录里有哪些子目录和文件」，
// 再逐层展开。这是「在目录里干活」的前提动作，外部客户端自己有 ls / glob。
//
// 默认跳过 node_modules/.git/dist 这类构建产物目录：一个前端项目根目录
// 光 node_modules 就能产出几万条，把上下文占满而没有任何信息量。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"quickdock/internal/fsguard"
	mcpsrv "quickdock/internal/mcp"
)

const (
	dirListDefaultLimit = 200  // dir_list 默认条数
	dirListMaxLimit     = 1000 // dir_list 条数上限
	dirListMaxDepth     = 3    // dir_list 展开层数上限：再深就该让模型自己一级一级看
)

func registerDir(d Deps) {
	r := registrar{d: d, category: mcpsrv.CategoryCode}
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(listDir, mcpsrv.LevelRead, "dir_list",
		"列出一个目录里有什么（名字、类型、大小、修改时间）。"+
			"这是「看项目结构」的工具：file_search 是按文件名全盘检索、不知道目录里有什么，"+
			"file_grep 是按内容找。默认忽略 node_modules/.git/dist 等构建产物目录，需要时用 includeIgnored 打开。"+
			"返回的 name 是相对该目录的路径，可直接喂给 file_read",
		[]string{"path"},
		map[string]any{
			"path":           str("目录路径：绝对路径，或相对当前工作目录的路径"),
			"glob":           str("可选。只列匹配该通配符的文件，如 *.go / *.ts（不影响目录）"),
			"depth":          intp("可选。展开层数，1 只列直接子项（默认），最大 3"),
			"limit":          intp("可选。最多返回多少条，默认 200，最大 1000"),
			"includeIgnored": mcpsrv.Bool("可选。是否连 node_modules/.git/dist 这类构建产物目录一起列，默认 false"),
		})
}

func listDir(d Deps, args map[string]any) (any, error) {
	root, err := fsguard.ResolveRead(d.DB, mcpsrv.PathArg(args, "path"), mcpsrv.Cwd(args), false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("无法访问 %s：%v", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s 是文件，不是目录；读文件用 file_read", root)
	}

	depth := mcpsrv.ArgInt(args, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > dirListMaxDepth {
		depth = dirListMaxDepth
	}
	limit := mcpsrv.ArgInt(args, "limit", dirListDefaultLimit)
	if limit <= 0 {
		limit = dirListDefaultLimit
	}
	if limit > dirListMaxLimit {
		limit = dirListMaxLimit
	}
	pattern := strings.TrimSpace(mcpsrv.Arg(args, "glob"))
	includeIgnored := mcpsrv.ArgBool(args, "includeIgnored")

	entries := make([]map[string]any, 0, 32)
	truncated := false
	var walk func(dir string, level int)
	walk = func(dir string, level int) {
		if truncated {
			return
		}
		items, err := os.ReadDir(dir)
		if err != nil {
			// 单层失败（权限不足、目录被占用）不该让整次列举失败：
			// 列一个项目根目录时撞见 .ssh 是很正常的事。
			return
		}
		for _, it := range items {
			if truncated {
				return
			}
			name := it.Name()
			isDir := it.IsDir()
			if isDir && !includeIgnored && fsguard.SkipDir(name) {
				continue
			}
			if pattern != "" && !isDir {
				// glob 只作用于文件：目录名不匹配 *.go 是正常的，过滤掉会让模型看不到目录。
				if ok, _ := filepath.Match(pattern, name); !ok {
					continue
				}
			}
			rel, rerr := filepath.Rel(root, filepath.Join(dir, name))
			if rerr != nil {
				continue
			}
			if len(entries) >= limit {
				truncated = true
				return
			}
			e := map[string]any{"name": rel, "type": "file"}
			if isDir {
				e["type"] = "dir"
			} else if fi, ierr := it.Info(); ierr == nil {
				e["size"] = fi.Size()
				e["modified"] = fi.ModTime().Format("2006-01-02 15:04")
			}
			entries = append(entries, e)
		}
		if level >= depth {
			return
		}
		for _, it := range items {
			if it.IsDir() && !(!includeIgnored && fsguard.SkipDir(it.Name())) {
				walk(filepath.Join(dir, it.Name()), level+1)
			}
		}
	}
	walk(root, 1)

	// 目录排前面：模型看结构时先知道「有哪些层」，比按字母混排有用得多。
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i]["type"] == "dir", entries[j]["type"] == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i]["name"].(string)) < strings.ToLower(entries[j]["name"].(string))
	})

	out := map[string]any{
		"path":      root,
		"count":     len(entries),
		"depth":     depth,
		"entries":   entries,
		"truncated": truncated,
		"tip":       "name 是相对该目录的路径，可以直接传给 file_read / file_edit",
	}
	if truncated {
		out["truncatedNote"] = fmt.Sprintf("已达 %d 条上限，还有更多；请缩小范围（传 glob 或子目录）或调大 limit", limit)
	}
	return out, nil
}
