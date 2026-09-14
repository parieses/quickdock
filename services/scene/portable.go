package scene

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/logger"
	"quickdock/services"
)

// 场景的声明式表示（导出为 quickdock-scene.json，可放进项目仓库随代码走）。
//
// 设计约束：只承载「声明」，不承载「身份」。id / workspaceId / usageCount /
// createdAt / updatedAt / favorite / unbound 一律不导出——它们要么在导入目标端
// 必须重新生成（id 冲突），要么是纯本机统计（usageCount 串味、favorite 是个人偏好）。
//
// 排列顺序单独说明：不导出 sort 字段——数组位置本身就是顺序，多带一个数字只会
// 出现「数字说 3、位置却在 0」的矛盾。数组顺序依赖 db 层默认排序
// （orderByClause 给出的 `ORDER BY sort ASC, created_at ASC`），导入端按数组下标
// 重排 sort，与导出顺序互为逆操作。portable_test.go 的往返用例锁住了这一点。

const (
	scenePackageSchema  = "quickdock/scene@1"
	scenePackageVersion = 1
)

// ScenePackage 一个可导出/导入的场景包。
type ScenePackage struct {
	Schema      string           `json:"$schema"`
	Version     int              `json:"version"`
	ExportedAt  string           `json:"exportedAt"`
	Scene       SceneSpec        `json:"scene"`
	Collections []CollectionSpec `json:"collections"`
}

// SceneSpec 场景自身的声明。
type SceneSpec struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	Color       string          `json:"color,omitempty"`
	Env         []SceneEnvEntry `json:"env"`
}

// CollectionSpec 集合声明（含其下条目）。
type CollectionSpec struct {
	Name         string     `json:"name"`
	Type         string     `json:"type"`
	Description  string     `json:"description,omitempty"`
	Icon         string     `json:"icon,omitempty"`
	Color        string     `json:"color,omitempty"`
	OpenStrategy string     `json:"openStrategy,omitempty"`
	Items        []ItemSpec `json:"items"`
}

// ItemSpec 条目声明。字段与 db.CollectionItem 的「声明性字段」一一对应，
// 刻意不含 icon 之外的运行时字段（pluginData 等属于插件私有状态，不跨机器搬）。
type ItemSpec struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	Value            string `json:"value"`
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	Tool             string `json:"tool,omitempty"`
	Args             string `json:"args,omitempty"`
	Icon             string `json:"icon,omitempty"`
	Color            string `json:"color,omitempty"`
	Remark           string `json:"remark,omitempty"`
}

// SceneImportResult 导入结果摘要，供前端回显「建了多少」。
type SceneImportResult struct {
	SceneID     string   `json:"sceneId"`
	SceneName   string   `json:"sceneName"`
	Collections int      `json:"collections"`
	Items       int      `json:"items"`
	SkippedEnv  []string `json:"skippedEnv"` // 目标端不存在/不支持服务的运行时
}

// buildScenePackage 把库里某场景组装成可序列化的包。
func (s *SceneService) buildScenePackage(sceneID string) (*ScenePackage, error) {
	sc, err := s.App.DB.GetScene(sceneID)
	if err != nil {
		return nil, err
	}
	colls, err := s.App.DB.ListCollections(sceneID)
	if err != nil {
		return nil, err
	}
	pkg := &ScenePackage{
		Schema:      scenePackageSchema,
		Version:     scenePackageVersion,
		ExportedAt:  time.Now().Format(time.RFC3339),
		Scene:       SceneSpec{Name: sc.Name, Type: sc.Type, Description: sc.Description, Icon: sc.Icon, Color: sc.Color, Env: parseSceneEnv(sc.Env)},
		Collections: make([]CollectionSpec, 0, len(colls)),
	}
	if pkg.Scene.Env == nil {
		pkg.Scene.Env = []SceneEnvEntry{} // 回传空数组而非 null，便于手写/对比
	}
	// ListCollections / ListItems 已由 db 层默认按 sort 升序返回，此处不再重复排序。
	for _, c := range colls {
		items, err := s.App.DB.ListItems(c.ID)
		if err != nil {
			return nil, err
		}
		cs := CollectionSpec{
			Name: c.Name, Type: c.Type, Description: c.Description,
			Icon: c.Icon, Color: c.Color, OpenStrategy: c.OpenStrategy,
			Items: make([]ItemSpec, 0, len(items)),
		}
		for _, it := range items {
			cs.Items = append(cs.Items, ItemSpec{
				Name: it.Name, Type: it.Type, Value: it.Value,
				WorkingDirectory: it.WorkingDirectory, Tool: it.Tool, Args: it.Args,
				Icon: it.Icon, Color: it.Color, Remark: it.Remark,
			})
		}
		pkg.Collections = append(pkg.Collections, cs)
	}
	return pkg, nil
}

// SceneExport 把场景导出为 JSON 文件到 path（由前端的保存对话框提供）。
func (s *SceneService) SceneExport(sceneID, path string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if path == "" {
		return services.FailMsg("导出路径为空")
	}
	pkg, err := s.buildScenePackage(sceneID)
	if err != nil {
		return services.Fail(err)
	}
	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return services.Fail(err)
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return services.FailMsg("创建导出目录失败: " + err.Error())
		}
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return services.FailMsg("写入文件失败: " + err.Error())
	}
	items := 0
	for _, c := range pkg.Collections {
		items += len(c.Items)
	}
	logger.I("[scene] 导出场景 %s → %s (集合 %d / 条目 %d)", sceneID, path, len(pkg.Collections), items)
	return services.Ok(map[string]interface{}{
		"path":        path,
		"collections": len(pkg.Collections),
		"items":       items,
	})
}

// SceneImport 从 JSON 文件导入场景到指定工作空间。
//
// 语义：**总是新建**，不做合并。理由——合并需要「按名字匹配 + 逐字段取舍」的冲突
// 策略，而条目 name 在集合内唯一，用户改过名就对不上，静默合并比新建更危险。
// 同名场景/集合由 dedupeName 追加后缀，导入永远不会因重名失败。
func (s *SceneService) SceneImport(workspaceID, path string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if workspaceID == "" {
		return services.FailMsg("未指定目标工作空间")
	}
	if path == "" {
		return services.FailMsg("导入路径为空")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return services.FailMsg("读取文件失败: " + err.Error())
	}
	var pkg ScenePackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return services.FailMsg("不是有效的场景文件: " + err.Error())
	}
	if pkg.Version > scenePackageVersion {
		return services.FailMsg(fmt.Sprintf("场景文件版本 %d 高于当前支持的 %d，请升级 QuickDock", pkg.Version, scenePackageVersion))
	}
	if strings.TrimSpace(pkg.Scene.Name) == "" {
		return services.FailMsg("场景文件缺少场景名称")
	}
	if _, err := s.App.DB.GetWorkspace(workspaceID); err != nil {
		return services.Fail(err)
	}

	// 场景名去重
	name, err := s.uniqueSceneName(workspaceID, pkg.Scene.Name)
	if err != nil {
		return services.Fail(err)
	}
	sceneType := pkg.Scene.Type
	if sceneType == "" {
		sceneType = "通用"
	}
	sc, err := s.App.DB.CreateScene(workspaceID, name, sceneType)
	if err != nil {
		return services.Fail(err)
	}
	// 场景的环境绑定：只保留目标端确实支持服务的运行时，其余记入 skippedEnv 回显，
	// 而不是整包失败——跨机器导入时目标端没装某个运行时是常态。
	skipped := []string{}
	envKeep := make([]SceneEnvEntry, 0, len(pkg.Scene.Env))
	known := s.serviceableRuntimes()
	seenRT := map[string]bool{}
	for _, e := range pkg.Scene.Env {
		if e.Runtime == "" || seenRT[e.Runtime] {
			continue
		}
		if !known[e.Runtime] {
			skipped = append(skipped, e.Runtime)
			continue
		}
		seenRT[e.Runtime] = true
		envKeep = append(envKeep, e)
	}
	envRaw, err := json.Marshal(envKeep)
	if err != nil {
		return services.Fail(err)
	}
	updates := map[string]interface{}{
		"description": pkg.Scene.Description,
		"icon":        pkg.Scene.Icon,
		"color":       pkg.Scene.Color,
		"env":         string(envRaw),
	}
	if err := s.App.DB.UpdateScene(sc.ID, updates); err != nil {
		return services.Fail(err)
	}

	collCount, itemCount := 0, 0
	for ci, cs := range pkg.Collections {
		collName := strings.TrimSpace(cs.Name)
		if collName == "" {
			collName = fmt.Sprintf("集合 %d", ci+1)
		}
		collType := cs.Type
		if collType == "" {
			collType = "目录集合"
		}
		c, err := s.App.DB.CreateCollection(workspaceID, sc.ID, collName, collType, cs.OpenStrategy)
		if err != nil {
			logger.W("QuickDock: 导入集合 %q 失败，跳过: %v", collName, err)
			continue
		}
		if err := s.App.DB.UpdateCollection(c.ID, map[string]interface{}{
			"description": cs.Description, "icon": cs.Icon, "color": cs.Color, "sort": ci,
		}); err != nil {
			logger.W("QuickDock: 导入集合 %q 附加字段失败: %v", collName, err)
		}
		collCount++
		for ii, it := range cs.Items {
			itName := strings.TrimSpace(it.Name)
			if itName == "" || it.Type == "" {
				continue
			}
			created, err := s.App.DB.CreateItem(workspaceID, c.ID, itName, it.Type, it.Value)
			if err != nil {
				logger.W("QuickDock: 导入条目 %q 失败，跳过: %v", itName, err)
				continue
			}
			extra := map[string]interface{}{"sort": ii}
			if it.WorkingDirectory != "" {
				extra["working_directory"] = it.WorkingDirectory
			}
			if it.Tool != "" {
				extra["tool"] = it.Tool
			}
			if it.Args != "" {
				extra["args"] = it.Args
			}
			if it.Icon != "" {
				extra["icon"] = it.Icon
			}
			if it.Color != "" {
				extra["color"] = it.Color
			}
			if it.Remark != "" {
				extra["remark"] = it.Remark
			}
			if err := s.App.DB.UpdateItem(created.ID, extra); err != nil {
				logger.W("QuickDock: 导入条目 %q 附加字段失败: %v", itName, err)
			}
			itemCount++
		}
	}
	logger.I("[scene] 导入场景 %q → 工作空间 %s (集合 %d / 条目 %d / 跳过环境 %v)", name, workspaceID, collCount, itemCount, skipped)
	return services.Ok(SceneImportResult{
		SceneID: sc.ID, SceneName: name,
		Collections: collCount, Items: itemCount, SkippedEnv: skipped,
	})
}

// uniqueSceneName 返回工作空间内未被占用的场景名：重名时依次尝试「名称 (导入)」
// 「名称 (导入 2)」…一个文件重复导入多次也不会失败。
func (s *SceneService) uniqueSceneName(workspaceID, base string) (string, error) {
	list, err := s.App.DB.ListScenes(workspaceID)
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(list))
	for _, sc := range list {
		taken[sc.Name] = true
	}
	if !taken[base] {
		return base, nil
	}
	first := base + " (导入)"
	if !taken[first] {
		return first, nil
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s (导入 %d)", base, i)
		if !taken[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("同名场景过多，无法生成唯一名称")
}
