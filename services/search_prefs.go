package services

import (
	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// 下面四个 Visible 函数是「资源树 / AI / MCP 读取」的统一过滤口径。
//
// 为什么不放在 DB 层：ListWorkspaces/ListScenes/ListCollections/ListItems 同时被
// 资源树的编辑界面使用——库层直接过滤会让勾了「不参与搜索」的项从界面消失，用户
// 再也点不到它、无法取消勾选（等于给自己造了一条数据黑洞）。过滤放在服务层，
// 由「展示 / 喂给模型」的调用方主动选择，编辑链路仍走原始全量读取。
//
// 规则（与命令面板 usePaletteData.ts 的口径一致）：
//   - 类别总开关关掉 → 该类整体不返回
//   - 单条 no_search=1 → 该条不返回
//   - 场景被排除时其下的集合、条目也一并排除（集合/条目挂在场景树上）
//   - 工作空间被排除时整个树都不返回
//   - showHidden 打开时全部放行（逃生口，见 internal/db/search.go）
//
// 方法导出供 MCP 子包调用：MCP 工具拿到的是「展示/喂给模型」的结果，必须过滤；
// 资源树的编辑界面仍走原始 service.List*，不受影响。
func (a *AppService) searchHidden() bool {
	return a.DB != nil && a.DB.GetSearchShowHidden()
}

func (a *AppService) VisibleWorkspaces(in []db.Workspace) []db.Workspace {
	if a.searchHidden() {
		return in
	}
	if a.DB.GetSearchExclusions().Workspaces {
		return []db.Workspace{}
	}
	out := make([]db.Workspace, 0, len(in))
	for _, w := range in {
		if w.NoSearch == 0 {
			out = append(out, w)
		}
	}
	return out
}

func (a *AppService) VisibleScenes(in []db.Scene) []db.Scene {
	if a.searchHidden() {
		return in
	}
	ex := a.DB.GetSearchExclusions()
	if ex.Workspaces || ex.Scenes {
		return []db.Scene{}
	}
	out := make([]db.Scene, 0, len(in))
	for _, s := range in {
		if s.NoSearch == 0 {
			out = append(out, s)
		}
	}
	return out
}

func (a *AppService) VisibleCollections(in []db.Collection) []db.Collection {
	if a.searchHidden() {
		return in
	}
	ex := a.DB.GetSearchExclusions()
	if ex.Workspaces || ex.Scenes {
		return []db.Collection{}
	}
	out := make([]db.Collection, 0, len(in))
	for _, c := range in {
		if c.NoSearch == 0 {
			out = append(out, c)
		}
	}
	return out
}

func (a *AppService) VisibleItems(in []db.CollectionItem) []db.CollectionItem {
	if a.searchHidden() {
		return in
	}
	if a.DB.GetSearchExclusions().Items {
		return []db.CollectionItem{}
	}
	return excludeNoSearchItems(in)
}

// Ctrl+K 搜索排除设置的绑定入口。
//
// 只负责读写三个类别开关；单条排除（workspace / scene / collection / item 上的
// no_search 列）随各自的 Update 接口一起保存，不在这里另开一条通道。
// 命令面板（独立窗口）与设置页（主窗口）各自持有自己的 WebView，localStorage
// 不共享，所以这份设置必须落库、经接口读，不能放前端偏好里。

// GetSearchExclusions 读取三类实体的全局搜索排除开关。
func (a *AppService) GetSearchExclusions() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	return Ok(a.DB.GetSearchExclusions())
}

// SaveSearchExclusions 保存全局搜索排除开关，返回落库后的实际值。
func (a *AppService) SaveSearchExclusions(p db.SearchExclusions) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetSearchExclusions(p); err != nil {
		return Fail(err)
	}
	logger.I("[搜索] 全局搜索排除已更新：工作空间=%v 场景集合=%v 项目=%v",
		p.Workspaces, p.Scenes, p.Items)
	return Ok(a.DB.GetSearchExclusions())
}

// GetSearchShowHidden 是否显示被标记 no_search 的项（资源树/AI/MCP）。
// 默认 false：排除生效；打开后隐藏项重新可见，用于取消勾选。
func (a *AppService) GetSearchShowHidden() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	return Ok(a.DB.GetSearchShowHidden())
}

// SetSearchShowHidden 设置是否显示隐藏项。
func (a *AppService) SetSearchShowHidden(on bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetSearchShowHidden(on); err != nil {
		return Fail(err)
	}
	logger.I("[搜索] 显示隐藏项 = %v", on)
	return Ok(on)
}
