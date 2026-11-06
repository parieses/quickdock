// Package shelf 是「文件置物架」的门面：只记录路径，不搬动用户文件。
//
// 与 collection/item 的区别：条目是「工作空间里要启动的东西」，有场景/工具/环境变量；
// 置物架是「今天临时要用的几个文件」，扁平一条清单，双击即开、拖进来即记。
// 两者数据模型和使用频率都不同，硬塞进 items 表只会让工作空间多出一堆无关条目。
package shelf

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/services"
)

const maxItems = 200

type ShelfService struct {
	App *services.AppService
}

func NewShelfService(app *services.AppService) *ShelfService {
	return &ShelfService{App: app}
}

// List 返回清单，并顺手标注路径是否还在（不落库：文件状态随时会变）。
//
// kind 也在这里按当前形态纠正一次：库里存的是入库那一刻的判断，用户可能先加了
// 一个还不存在的路径、后来把它建成了目录。图标与「打开」行为都靠 kind，
// 一旦和磁盘对不上，界面上就会用文件的图标去表示一个文件夹。
func (s *ShelfService) List() *services.ApiResult {
	items, err := s.App.DB.ListShelf()
	if err != nil {
		return services.Fail(err)
	}
	for i := range items {
		info, statErr := os.Stat(items[i].Path)
		if statErr != nil {
			continue
		}
		items[i].Exists = true
		if info.IsDir() {
			items[i].Kind = "dir"
		} else {
			items[i].Kind = "file"
		}
	}
	return services.Ok(items)
}

// AddPaths 由「拖文件进窗口」和「手动填路径」共用；重复路径自动忽略。
func (s *ShelfService) AddPaths(paths []string) *services.ApiResult {
	if len(paths) == 0 {
		return services.Ok(0)
	}
	if len(paths) > maxItems {
		paths = paths[:maxItems]
	}
	items := make([]db.ShelfItem, 0, len(paths))
	for _, raw := range paths {
		if it, err := normalize(raw); err == nil {
			items = append(items, it)
		}
	}
	return services.Wrap(s.App.DB.AddShelfPaths(items))
}

// AddPath 是「选择文件」按钮的单条入口，避免前端为一条路径构造数组。
func (s *ShelfService) AddPath(path string) *services.ApiResult {
	return s.AddPaths([]string{path})
}

func (s *ShelfService) Remove(id string) *services.ApiResult {
	return services.Wrap(true, s.App.DB.RemoveShelf(id))
}

func (s *ShelfService) Reorder(ids []string) *services.ApiResult {
	if len(ids) > maxItems {
		return services.FailMsg("置物架条目过多")
	}
	return services.Wrap(true, s.App.DB.ReorderShelf(ids))
}

// Open 打开条目，同时记一次使用时间。
//
// 目录不走 ShellOpen：后者依赖系统文件关联，而「目录」在部分机器上没有稳定关联
// （Wails 的 Browser.OpenFile 对目录时灵时不灵）。RevealInExplorer 对目录的处理
// 本来就是「打开这个目录」，语义与结果都确定。
func (s *ShelfService) Open(id string) *services.ApiResult {
	it, err := s.App.DB.GetShelf(id)
	if err != nil {
		return services.Fail(err)
	}
	if it == nil {
		return services.FailMsg("条目不存在")
	}
	if info, statErr := os.Stat(it.Path); statErr == nil && info.IsDir() {
		if err := platform.RevealInExplorer(it.Path); err != nil {
			return services.Fail(fmt.Errorf("打开失败: %w", err))
		}
		return services.Wrap(true, s.App.DB.TouchShelf(id))
	}
	if err := platform.ShellOpen(it.Path, ""); err != nil {
		return services.Fail(fmt.Errorf("打开失败: %w", err))
	}
	return services.Wrap(true, s.App.DB.TouchShelf(id))
}

// Reveal 在资源管理器中定位（文件不存在时平台层会退回打开父目录）。
func (s *ShelfService) Reveal(id string) *services.ApiResult {
	it, err := s.App.DB.GetShelf(id)
	if err != nil {
		return services.Fail(err)
	}
	if it == nil {
		return services.FailMsg("条目不存在")
	}
	return services.Wrap(true, platform.RevealInExplorer(it.Path))
}

// normalize 把外部传进来的路径统一成本机绝对路径。
// 资源管理器复制 / Wails 拖放给的都是绝对路径，这里仍做一次清洗：
// 去掉外层引号、支持 file:// URL、拒绝空串与危险协议（ShellOpen 内部还有第二道）。
func normalize(raw string) (db.ShelfItem, error) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, `"`)
	if p == "" {
		return db.ShelfItem{}, fmt.Errorf("空路径")
	}
	if strings.HasPrefix(strings.ToLower(p), "file://") {
		u, err := url.Parse(p)
		if err != nil {
			return db.ShelfItem{}, err
		}
		if u.Host != "" && u.Host != "localhost" {
			return db.ShelfItem{}, fmt.Errorf("不支持远程路径")
		}
		p = u.Path
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:] // /C:/foo → C:/foo
		}
	}
	for _, bad := range []string{"javascript:", "vbscript:", "ms-msdt:", "ms-powershell:"} {
		if strings.HasPrefix(strings.ToLower(p), bad) {
			return db.ShelfItem{}, fmt.Errorf("不支持的协议")
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return db.ShelfItem{}, err
	}
	abs = filepath.Clean(abs)
	kind := "file"
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		kind = "dir"
	}
	return db.ShelfItem{Path: abs, Name: filepath.Base(abs), Kind: kind}, nil
}
