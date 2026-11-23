package script

import (
	"os"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/env"
	"quickdock/internal/platform"
	"quickdock/internal/scriptrun"
	"quickdock/services"
)

// MaskedValue 是 secret 变量回给前端时的占位。
//
// 为什么用占位而不是空串：空串既表示「没值」也表示「不想给你看」，
// 用户点开编辑框看到空值会以为变量丢了，一保存就把真实值清空了。
const MaskedValue = "••••••••"

// locateRuntime 返回 QuickDock 托管运行时的 bin 目录，供解释器解析与 PATH 前置。
//
// 顺序统一是「托管运行时优先，找不到再退系统 PATH」（见 scriptrun.Resolve）：
// 用户在「环境管理」里装了就用那份，没装则用系统里已有的、PATH 里能找到的。
func (s *ScriptService) locateRuntime(lang string) string {
	if s.App.Env == nil {
		return ""
	}
	switch lang {
	case scriptrun.LangPython:
		return s.App.Env.BinDirFor(env.RuntimePython, "")
	case scriptrun.LangJavaScript:
		return s.App.Env.BinDirFor(env.RuntimeNode, "")
	case scriptrun.LangGo:
		return s.App.Env.BinDirFor(env.RuntimeGo, "")
	case scriptrun.LangPHP:
		return s.App.Env.BinDirFor(env.RuntimePHP, "")
	}
	return ""
}

// composerBinDir 返回托管 Composer 的目录（里面是 composer.phar，不是可执行文件）。
func (s *ScriptService) composerBinDir() string {
	if s.App.Env == nil {
		return ""
	}
	return s.App.Env.BinDirFor(env.RuntimeComposer, "")
}

// buildEnv 组装子进程环境：系统环境 → 全局变量 → 脚本变量 → 内置变量。
//
// 是「合并」不是「替换」：cmd.Env 一旦赋值就完全取代继承环境，直接给三五个
// 变量会让脚本连 PATH 都没有，`subprocess.run(["curl"])` 当场失败。
func (s *ScriptService) buildEnv(sc *db.Script, runID string) ([]string, error) {
	merged := make(map[string]string, 64)
	order := make([]string, 0, 64)
	set := func(k, v string) {
		if _, ok := merged[k]; !ok {
			order = append(order, k)
		}
		merged[k] = v
	}

	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set(kv[:i], kv[i+1:])
		}
	}

	list, err := s.App.DB.ListScriptEnvs(sc.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range list {
		set(e.Key, e.Value)
	}

	// 内置变量最后写，且脚本级同名变量不许覆盖：用户若配了一个 QD_SCRIPT_DIR，
	// 脚本里 os.chdir(QD_SCRIPT_DIR) 就会跑到别的目录去，而这种错查起来极慢。
	dir := s.dir(sc.ID)
	set("QD_SCRIPT_ID", sc.ID)
	set("QD_SCRIPT_NAME", sc.Name)
	set("QD_SCRIPT_LANG", sc.Lang)
	set("QD_SCRIPT_DIR", dir)
	set("QD_RUN_ID", runID)
	set("QD_DATA_DIR", platform.DefaultDataDir())

	// PATH 前置运行时 bin 目录：脚本里直接调 `pip` / `node` / `npx` 才找得到，
	// 否则用户还得自己写全路径。
	if bin := s.locateRuntime(sc.Lang); bin != "" {
		setPath(merged, &order, bin)
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+merged[k])
	}
	return out, nil
}

// setPath 把 dir 前置到 PATH。
//
// Windows 上 os.Environ() 给的键名通常是 Path 而不是 PATH，直接 set("PATH")
// 会造出两条键；Go 的 exec 在 Windows 会按大小写不敏感去重、保留最后一条，
// 所以必须先把已有的那条改名到最后，而不是新增一条同义键。
func setPath(merged map[string]string, order *[]string, dir string) {
	key := "PATH"
	for k := range merged {
		if strings.EqualFold(k, "PATH") {
			key = k
			break
		}
	}
	old := merged[key]
	if old == "" {
		merged[key] = dir
	} else {
		merged[key] = dir + string(os.PathListSeparator) + old
	}
	if _, ok := merged[key]; ok {
		// 移到末尾：写入顺序就是最终顺序，重复键时后者生效。
		moved := make([]string, 0, len(*order)+1)
		for _, k := range *order {
			if k != key {
				moved = append(moved, k)
			}
		}
		*order = append(moved, key)
	}
}

// ---------- 变量 CRUD ----------

// EnvInput 变量保存入参。
type EnvInput struct {
	ID       string `json:"id"`
	ScriptID string `json:"scriptId"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Secret   bool   `json:"secret"`
	Remark   string `json:"remark"`
	Sort     int    `json:"sort"`
}

// ListScriptEnvs 列表（secret 值脱敏）。
func (s *ScriptService) ListScriptEnvs(scriptID string) *services.ApiResult {
	list, err := s.App.DB.ListScriptEnvs(scriptID)
	if err != nil {
		return services.Fail(err)
	}
	out := make([]db.ScriptEnv, len(list))
	copy(out, list)
	for i := range out {
		if out[i].Secret {
			out[i].Value = MaskedValue
		}
	}
	return services.Ok(out)
}

// SaveScriptEnv 新建/更新变量。
func (s *ScriptService) SaveScriptEnv(in EnvInput) *services.ApiResult {
	if in.ScriptID != "" {
		if _, err := s.App.DB.GetScript(in.ScriptID); err != nil {
			return services.Fail(err)
		}
	}
	// 未修改的 secret 值：前端拿到的是掩码，原样提交回来时必须保留旧值，
	// 否则「点开变量什么都没改、保存一下」就把 Cookie 清了。
	if in.Secret && in.Value == MaskedValue {
		if in.ID != "" {
			if old, err := s.App.DB.GetScriptEnv(in.ID); err == nil {
				in.Value = old.Value
			}
		} else if old, err := s.App.DB.ListScriptEnvs(in.ScriptID); err == nil {
			for _, e := range old {
				if e.Key == in.Key && e.ScriptID == in.ScriptID {
					in.Value = e.Value
					break
				}
			}
		}
	}
	row, err := s.App.DB.UpsertScriptEnv(&db.ScriptEnv{
		ID: in.ID, ScriptID: in.ScriptID, Key: in.Key, Value: in.Value,
		Secret: in.Secret, Remark: in.Remark, Sort: in.Sort,
	})
	if err != nil {
		return services.Fail(err)
	}
	if row.Secret {
		row.Value = MaskedValue
	}
	s.emit(EventChanged, map[string]any{"scriptId": in.ScriptID, "reason": "env"})
	return services.Ok(row)
}

// RevealScriptEnv 显示一条变量的明文（眼睛按钮）。
//
// 单独一个接口而不是让列表直接给明文：列表会被截图、会被投屏、会进日志。
func (s *ScriptService) RevealScriptEnv(id string) *services.ApiResult {
	e, err := s.App.DB.GetScriptEnv(id)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"id": e.ID, "value": e.Value})
}

// DeleteScriptEnv 删除变量。
func (s *ScriptService) DeleteScriptEnv(id string) *services.ApiResult {
	e, err := s.App.DB.GetScriptEnv(id)
	if err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.DeleteScriptEnv(id); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": e.ScriptID, "reason": "env"})
	return services.Ok(nil)
}
