// Package scene 场景门面服务。
package scene

import (
	"encoding/json"
	"fmt"
	"sort"

	"quickdock/internal/env"
	"quickdock/internal/logger"
	"quickdock/services"
)

// SceneEnvEntry 场景绑定的单个环境服务（持久化在 scenes.env 的 JSON 数组里）。
type SceneEnvEntry struct {
	Runtime string `json:"runtime"` // 运行时 id，如 mysql / redis
	Version string `json:"version"` // 期望版本；空=跟随当前激活版本
}

// SceneEnvStatus 应用场景环境后的逐项结果，供前端回显「哪个起来了、哪个失败」。
type SceneEnvStatus struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// parseSceneEnv 解析 scenes.env 字段；空串/非法 JSON 都视为「未绑定」，不报错。
func parseSceneEnv(raw string) []SceneEnvEntry {
	if raw == "" {
		return nil
	}
	var out []SceneEnvEntry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.W("QuickDock: 解析场景环境绑定失败: %v", err)
		return nil
	}
	return out
}

// SceneService 场景 CRUD 绑定服务。
type SceneService struct {
	App *services.AppService
}

// NewSceneService 创建 SceneService。
func NewSceneService(app *services.AppService) *SceneService {
	return &SceneService{App: app}
}

func (s *SceneService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// ListScenes 列出某工作空间下的全部场景。
func (s *SceneService) ListScenes(workspaceID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.ListScenes(workspaceID)
	return services.Wrap(data, err)
}

// CreateScene 新建场景。
func (s *SceneService) CreateScene(workspaceID, name, sceneType string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	data, err := s.App.DB.CreateScene(workspaceID, name, sceneType)
	return services.Wrap(data, err)
}

// UpdateScene 更新场景。
func (s *SceneService) UpdateScene(id string, updates map[string]interface{}) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateScene(id, updates); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteScene 删除场景。
func (s *SceneService) DeleteScene(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteScene(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ReorderScenes 排序场景。
func (s *SceneService) ReorderScenes(orderedIDs []string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.Reorder("scenes", orderedIDs); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// serviceableRuntimes 返回支持服务启停的运行时 id 集合。
// 无服务能力的运行时（如 Node / Git / Composer）无法启停，不允许绑进场景。
func (s *SceneService) serviceableRuntimes() map[string]bool {
	out := map[string]bool{}
	if s.App.Env == nil {
		return out
	}
	for _, ri := range s.App.Env.List() {
		if ri.HasService {
			out[ri.ID] = true
		}
	}
	return out
}

// SceneEnvList 读取某场景绑定的环境服务列表。
func (s *SceneService) SceneEnvList(sceneID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	sc, err := s.App.DB.GetScene(sceneID)
	if err != nil {
		return services.Fail(err)
	}
	entries := parseSceneEnv(sc.Env)
	if entries == nil {
		entries = []SceneEnvEntry{} // 回传空数组而非 null，前端不必判空
	}
	return services.Ok(entries)
}

// SceneEnvSave 保存某场景的环境服务绑定。
// entriesJSON 是 SceneEnvEntry 数组的 JSON 串——用字符串而非结构体切片传参，
// 是因为 Wails 绑定对基础类型最稳，前端 JSON.stringify 后直接传入即可。
func (s *SceneService) SceneEnvSave(sceneID, entriesJSON string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	var entries []SceneEnvEntry
	if entriesJSON != "" {
		if err := json.Unmarshal([]byte(entriesJSON), &entries); err != nil {
			return services.FailMsg("环境服务数据格式错误")
		}
	}
	known := s.serviceableRuntimes()
	clean := make([]SceneEnvEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Runtime == "" || seen[e.Runtime] {
			continue // 空 id 或同一运行时重复绑定，直接丢弃
		}
		if !known[e.Runtime] {
			return services.FailMsg(fmt.Sprintf("运行时 %s 不支持服务管理，不能绑定到场景", e.Runtime))
		}
		seen[e.Runtime] = true
		clean = append(clean, e)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.UpdateScene(sceneID, map[string]interface{}{"env": string(raw)}); err != nil {
		return services.Fail(err)
	}
	return services.Ok(clean)
}

// SceneEnvApply 应用场景环境：拉起本场景绑定的服务，并停掉「同工作空间其它场景绑定过、
// 但本场景未绑定」的服务——只拉不停的话，切几次场景后后台就堆满用不上的进程了。
//
// 已设为「常驻」的运行时会被跳过：常驻是用户显式表达的全局意图，优先级高于场景。
// 返回值是逐项结果，前端据此回显哪个起来了、哪个失败。
func (s *SceneService) SceneEnvApply(sceneID string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if s.App.Env == nil {
		return services.FailMsg("环境管理器未初始化")
	}
	sc, err := s.App.DB.GetScene(sceneID)
	if err != nil {
		return services.Fail(err)
	}
	want := parseSceneEnv(sc.Env)
	wantSet := make(map[env.Runtime]string, len(want))
	for _, e := range want {
		wantSet[env.Runtime(e.Runtime)] = e.Version
	}

	// 被场景托管的运行时全集 = 同工作空间所有场景绑定的并集
	all, err := s.App.DB.ListScenes(sc.WorkspaceID)
	if err != nil {
		return services.Fail(err)
	}
	managedSet := map[env.Runtime]bool{}
	for _, o := range all {
		for _, e := range parseSceneEnv(o.Env) {
			managedSet[env.Runtime(e.Runtime)] = true
		}
	}
	managed := make([]env.Runtime, 0, len(managedSet))
	for rt := range managedSet {
		managed = append(managed, rt)
	}
	sort.Slice(managed, func(i, j int) bool { return managed[i] < managed[j] }) // 固定顺序，输出可预期

	results := make([]SceneEnvStatus, 0, len(want)+len(managed))

	for _, e := range want {
		rt := env.Runtime(e.Runtime)
		ver, err := s.App.Env.ResolveVersion(rt, e.Version)
		if err != nil {
			results = append(results, SceneEnvStatus{Runtime: e.Runtime, Error: err.Error()})
			continue
		}
		if st, e2 := s.App.Env.Status(rt, ver); e2 == nil && st.Running {
			results = append(results, SceneEnvStatus{Runtime: e.Runtime, Version: ver, Running: true})
			continue
		}
		if err := s.App.Env.Start(rt, ver, nil); err != nil {
			logger.W("QuickDock: 场景应用启动 %s 失败: %v", rt, err)
			results = append(results, SceneEnvStatus{Runtime: e.Runtime, Version: ver, Error: err.Error()})
			continue
		}
		results = append(results, SceneEnvStatus{Runtime: e.Runtime, Version: ver, Running: true})
	}

	for _, rt := range managed {
		if _, ok := wantSet[rt]; ok {
			continue
		}
		if s.App.Env.Enabled(rt) {
			continue
		}
		ver, err := s.App.Env.ResolveVersion(rt, "")
		if err != nil {
			continue
		}
		if st, e2 := s.App.Env.Status(rt, ver); e2 == nil && !st.Running {
			continue
		}
		_ = s.App.Env.Stop(rt, ver) // 停止失败不阻断其余项
	}
	return services.Ok(results)
}
