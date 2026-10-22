//go:build windows

package env

import (
	"path/filepath"
	"sort"
	"strings"

	winreg "golang.org/x/sys/windows/registry"
)

// UserEnvList 列出当前用户（HKCU\Environment）的全部环境变量。
// 只读，不触碰系统级（HKLM）——系统级需要管理员权限，也不该由 QuickDock 擅自改。
func UserEnvList() (UserEnvReport, error) {
	rep := UserEnvReport{Vars: []UserEnvVar{}}
	k, err := winreg.OpenKey(winreg.CURRENT_USER, `Environment`, winreg.READ)
	if err != nil {
		// 键不存在等价于「没有任何用户级变量」，不是错误
		if err == winreg.ErrNotExist {
			return rep, nil
		}
		return rep, err
	}
	defer k.Close()

	names, err := k.ReadValueNames(-1)
	if err != nil {
		return rep, err
	}
	for _, name := range names {
		val, typ, e := k.GetStringValue(name)
		if e != nil {
			continue // 非字符串类型（如 REG_BINARY）直接跳过，QuickDock 不负责编辑
		}
		if strings.EqualFold(name, "PATH") {
			rep.Path = UserEnvPath{
				Value:   val,
				Expand:  typ == winreg.EXPAND_SZ,
				Entries: splitPath(val),
			}
			continue
		}
		rep.Vars = append(rep.Vars, UserEnvVar{
			Name:    name,
			Value:   val,
			Expand:  typ == winreg.EXPAND_SZ,
			Managed: managedEnvNames[strings.ToUpper(name)] && isManagedValue(val),
		})
	}
	sort.Slice(rep.Vars, func(i, j int) bool {
		// QuickDock 写入的排前面，其余按名称排序——排查时第一眼要看的就是自己那条
		if rep.Vars[i].Managed != rep.Vars[j].Managed {
			return rep.Vars[i].Managed
		}
		return strings.ToUpper(rep.Vars[i].Name) < strings.ToUpper(rep.Vars[j].Name)
	})
	return rep, nil
}

// UserEnvSetVar 新增或修改一条当前用户环境变量。
// value 为空字符串表示删除（与 sysSetEnvVar 的口径一致）。
func UserEnvSetVar(name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errEmptyEnvName
	}
	if strings.EqualFold(name, "PATH") {
		return errPathNotAllowed
	}
	return sysSetEnvVar(name, value)
}

// UserEnvDeleteVar 删除一条当前用户环境变量。
func UserEnvDeleteVar(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errEmptyEnvName
	}
	if strings.EqualFold(name, "PATH") {
		return errPathNotAllowed
	}
	return sysSetEnvVar(name, "")
}

// UserEnvSetPath 用给定的条目列表整体重写当前用户 PATH。
//
// 整体重写而不是逐条增删：UI 上用户可能一次改多条，逐条调用会反复读改写注册表，
// 中途失败会留下半套配置。写入前清掉空条目并去重，保留原有 REG_EXPAND_SZ 类型。
func UserEnvSetPath(entries []string) error {
	cleaned := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(e))
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, e)
	}

	k, err := openUserEnv()
	if err != nil {
		return err
	}
	defer k.Close()
	_, typ := readPathValue(k)
	if err := writePathValue(k, strings.Join(cleaned, ";"), typ); err != nil {
		return err
	}
	broadcastEnvChange()
	return nil
}
