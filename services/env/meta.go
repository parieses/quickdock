package env

import (
	"encoding/json"
	"os"
	"path/filepath"

	"quickdock/internal/platform"
)

// versionMeta 单版本的用户元数据：别名与备注
type versionMeta struct {
	Alias string `json:"alias"`
	Note  string `json:"note"`
}

// runtimeMeta 某运行时的元数据：当前激活（环境变量指向）的版本 + 各版本 alias/note
type runtimeMeta struct {
	Active   string                 `json:"active"`
	Versions map[string]versionMeta `json:"versions"`
}

func metaPath(rt Runtime) string {
	return filepath.Join(platform.DefaultDataDir(), "runtime", string(rt), ".qd-meta.json")
}

func loadMeta(rt Runtime) runtimeMeta {
	var m runtimeMeta
	data, err := os.ReadFile(metaPath(rt))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	if m.Versions == nil {
		m.Versions = map[string]versionMeta{}
	}
	return m
}

func saveMeta(rt Runtime, m runtimeMeta) error {
	if m.Versions == nil {
		m.Versions = map[string]versionMeta{}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := metaPath(rt)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// writeActiveMeta 将某运行时的激活版本写入元数据文件（其 bin 目录即“环境变量指向”的版本）。
// version=="" 表示清除激活。与 Manager.SetActive 方法同名易混，故显式命名以区分。
func writeActiveMeta(rt Runtime, version string) error {
	m := loadMeta(rt)
	m.Active = version
	return saveMeta(rt, m)
}

// SetVersionMeta 更新某版本的别名与备注
func SetVersionMeta(rt Runtime, version, alias, note string) error {
	m := loadMeta(rt)
	if m.Versions == nil {
		m.Versions = map[string]versionMeta{}
	}
	m.Versions[version] = versionMeta{Alias: alias, Note: note}
	return saveMeta(rt, m)
}

// ClearVersionMeta 删除某版本的别名/备注记录（删除版本时调用），若该版本为激活版本则一并清除激活
func ClearVersionMeta(rt Runtime, version string) error {
	m := loadMeta(rt)
	if m.Versions != nil {
		delete(m.Versions, version)
	}
	if m.Active == version {
		m.Active = ""
	}
	return saveMeta(rt, m)
}

func activeVersion(rt Runtime) string {
	return loadMeta(rt).Active
}

func versionMetaOf(rt Runtime, version string) versionMeta {
	return loadMeta(rt).Versions[version]
}
