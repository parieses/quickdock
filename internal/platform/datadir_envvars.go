package platform

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 环境变量在数据目录迁移时的处理：删除指向旧目录的项，设置指向新目录的项。
//
// 与 RewriteEnvVarsForDataDir（纯字符串替换）的区别：
//   - PATH 是分号分隔的**列表**，纯替换会在新旧路径同时存在时产生重复条目
//     （例如旧路径 D:\.quickdock 与新路径 E:\.quickdock，替换后可能出现两条）。
//     这里改为「按条目删除旧的 → 按条目追加新的 → 去重」。
//   - 其它变量（GOROOT/GOPATH/GOENV 等）值里只有一个路径，替换即可；
//     但若替换后新旧路径都在值里（如 GOROOT 里写了两个版本目录），也要清理。
//
// 原则：**只动指向旧数据目录的项**，其余用户自己的环境变量一律不碰。

// EnvVarChange 一条环境变量的变更记录（供 UI 展示与调试）。
type EnvVarChange struct {
	Name    string `json:"name"`
	Old     string `json:"old"`
	New     string `json:"new"`
	Removed bool   `json:"removed"` // true=已删除该变量，false=改成了新值
}

// EnvVarResult 环境变量处理结果。
type EnvVarResult struct {
	Changes []EnvVarChange `json:"changes"`
}

// pathListVars 这些变量的值是分号分隔的路径列表，需要按条目处理。
var pathListVars = map[string]bool{
	"PATH": true,
}

// ApplyEnvVarsForDataDir 把指向旧数据目录的环境变量迁移到新目录：
// 先删除指向旧目录的项，再设置指向新目录的项。
//
// oldDir/newDir 为空或相同时不做任何事。
//
// 两条路径：
//   - PATH（分号分隔的**列表**）：逐条目把「旧目录前缀」换成「新目录前缀」。
//     例如 C:\Users\X\.quickdock\runtime\go\1.27.2\bin
//     → D:\.quickdock\runtime\go\1.27.2\bin。
//     换前缀而不是「删旧 + 加数据目录本身」——后者会把 D:\.quickdock 塞进 PATH，
//     而数据目录不是可执行目录，PATH 里留着它毫无意义；真正需要指向的是它下面的 bin。
//   - 其它变量（GOROOT/GOPATH/GOENV 等）：值里只有一个路径，整值替换即可。
//
// 原则：**只动指向旧数据目录的项**，其余用户自己的环境变量一律不碰。
func ApplyEnvVarsForDataDir(oldDir, newDir string) (EnvVarResult, error) {
	res := EnvVarResult{}
	if oldDir == "" || newDir == "" {
		return res, nil
	}
	oldDir = filepath.Clean(oldDir)
	newDir = filepath.Clean(newDir)
	if strings.EqualFold(oldDir, newDir) {
		return res, nil
	}

	vars, err := readUserEnvVars()
	if err != nil {
		return res, err
	}
	for name, value := range vars {
		if value == "" {
			continue
		}
		if pathListVars[strings.ToUpper(name)] {
			// 列表型：逐条目换前缀（旧条目 → 新条目），再去重。
			entries := splitPathList(value)
			kept := make([]string, 0, len(entries))
			changed := false
			for _, e := range entries {
				if e == "" {
					continue
				}
				if ne, ok := replacePathPrefix(e, oldDir, newDir); ok {
					changed = true
					e = ne
				}
				if containsPathEntry(kept, e) {
					continue // 去重：换前缀后可能与已有条目重复
				}
				kept = append(kept, e)
			}
			if !changed {
				continue // 该变量本来就不指向旧目录，不动
			}
			newValue := strings.Join(kept, ";")
			if newValue == "" {
				// 删光了 → 整个变量删除，而不是留一个空值。
				if err := setUserEnvVar(name, ""); err != nil {
					return res, fmt.Errorf("删除环境变量 %s 失败: %w", name, err)
				}
				res.Changes = append(res.Changes, EnvVarChange{Name: name, Old: value, Removed: true})
				continue
			}
			if err := setUserEnvVar(name, newValue); err != nil {
				return res, fmt.Errorf("改写环境变量 %s 失败: %w", name, err)
			}
			res.Changes = append(res.Changes, EnvVarChange{Name: name, Old: value, New: newValue})
			continue
		}

		// 普通变量：值里含旧目录 → 整值替换为新目录。
		if !strings.Contains(strings.ToLower(value), strings.ToLower(oldDir)) {
			continue
		}
		newValue := replacePathPrefixAll(value, oldDir, newDir)
		if err := setUserEnvVar(name, newValue); err != nil {
			return res, fmt.Errorf("改写环境变量 %s 失败: %w", name, err)
		}
		res.Changes = append(res.Changes, EnvVarChange{Name: name, Old: value, New: newValue})
	}
	return res, nil
}

// replacePathPrefix 把 entry 的旧目录前缀换成新目录前缀。
// entry 不在 oldDir 之下时返回 ok=false。
//
// 大小写不敏感（Windows 路径不区分大小写），但保留原串其余部分的大小写。
func replacePathPrefix(entry, oldDir, newDir string) (string, bool) {
	e := strings.TrimSpace(entry)
	if e == "" {
		return entry, false
	}
	// 保留结尾的分隔符语义：旧目录之后的子路径原样接上。
	lower := strings.ToLower(e)
	lowerOld := strings.ToLower(oldDir)
	if lower == lowerOld {
		return newDir, true
	}
	if strings.HasPrefix(lower, lowerOld+string(filepath.Separator)) ||
		strings.HasPrefix(lower, lowerOld+"/") {
		rest := e[len(oldDir):]
		return newDir + rest, true
	}
	return entry, false
}

// replacePathPrefixAll 把 value 里所有出现的旧目录前缀换成新目录前缀。
func replacePathPrefixAll(value, oldDir, newDir string) string {
	lower := strings.ToLower(value)
	lowerOld := strings.ToLower(oldDir)
	var b strings.Builder
	for {
		i := strings.Index(lower, lowerOld)
		if i < 0 {
			b.WriteString(value)
			break
		}
		b.WriteString(value[:i])
		b.WriteString(newDir)
		value = value[i+len(oldDir):]
		lower = lower[i+len(oldDir):]
	}
	return b.String()
}

// splitPathList 按分号切分路径列表（Windows 路径分隔符）。
func splitPathList(v string) []string {
	return strings.Split(v, ";")
}

// containsPathEntry 判断列表里是否已有该条目（忽略大小写与尾部斜杠）。
func containsPathEntry(list []string, target string) bool {
	for _, e := range list {
		if samePath(e, target) {
			return true
		}
	}
	return false
}

// samePath 比较两个路径是否等价（大小写不敏感、忽略尾部斜杠）。
func samePath(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\/`), strings.TrimRight(b, `\/`))
}

// pathUnderDir 判断某个路径条目是否位于 dir 之下（含相等）。
func pathUnderDir(entry, dir string) bool {
	e := filepath.Clean(strings.TrimSpace(entry))
	d := filepath.Clean(dir)
	if samePath(e, d) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(e), strings.ToLower(d)+string(filepath.Separator))
}
