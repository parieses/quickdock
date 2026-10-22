package env

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 环境清单（manifest）导入/导出（2026-10 引入）。
//
// 场景：团队里每个人的机器都该跑同一套版本（Node 22 / PHP 8.4 / Go 1.27 …），
// 但版本散落在各自的 QuickDock 里，只能靠口头同步。导出把当前环境落成一份 JSON，
// 发给同事后导入即可看到「我缺哪些版本、哪个版本没激活」。
//
// 导入刻意**不自动下载/删除**任何东西：装哪个版本涉及体积与兼容性判断，
// 不该由一份外部文件替用户决定；applyActive=true 时也只对「本机已装」的版本生效。

// ManifestFormat 清单格式版本。后续若字段不兼容变更，据此拒绝或迁移。
const ManifestFormat = 1

// Manifest 一份环境清单。
type Manifest struct {
	Format     int               `json:"format"`
	ExportedAt string            `json:"exportedAt"`
	AppVersion string            `json:"appVersion"`
	Runtimes   []ManifestRuntime `json:"runtimes"`
}

// ManifestRuntime 清单里的单个运行时。
type ManifestRuntime struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ActiveSource string            `json:"activeSource,omitempty"`
	CustomSource string            `json:"customSource,omitempty"`
	Versions     []ManifestVersion `json:"versions"`
}

// ManifestVersion 清单里的单个版本。
type ManifestVersion struct {
	Version string `json:"version"`
	Active  bool   `json:"active"`
	Alias   string `json:"alias,omitempty"`
	Note    string `json:"note,omitempty"`
}

// BuildManifest 由当前运行时列表构造清单（只导出已装版本，未装的运行时跳过）。
func BuildManifest(appVersion string, list []RuntimeInfo) Manifest {
	m := Manifest{
		Format:     ManifestFormat,
		ExportedAt: time.Now().Format(time.RFC3339),
		AppVersion: appVersion,
		Runtimes:   make([]ManifestRuntime, 0, len(list)),
	}
	for _, info := range list {
		if len(info.Installed) == 0 {
			continue
		}
		mr := ManifestRuntime{
			ID:           info.ID,
			Name:         info.Name,
			ActiveSource: info.ActiveSource,
			CustomSource: CustomSource(Runtime(info.ID)),
		}
		for _, ins := range info.Installed {
			mr.Versions = append(mr.Versions, ManifestVersion{
				Version: ins.Version,
				Active:  ins.Active,
				Alias:   ins.Alias,
				Note:    ins.Note,
			})
		}
		m.Runtimes = append(m.Runtimes, mr)
	}
	return m
}

// ParseManifest 解析并校验清单。仅接受本应用导出的格式（format 字段必须存在）。
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("清单不是合法的 JSON: %w", err)
	}
	if m.Format <= 0 {
		return m, fmt.Errorf("不是有效的环境清单（缺少 format 字段）")
	}
	if m.Format > ManifestFormat {
		return m, fmt.Errorf("清单由更新版本的 QuickDock 导出（format=%d），请升级后再导入", m.Format)
	}
	if len(m.Runtimes) == 0 {
		return m, fmt.Errorf("清单里没有任何运行时版本")
	}
	return m, nil
}

// ImportDiff 单个运行时的比对结果。
type ImportDiff struct {
	Runtime       string   `json:"runtime"`
	Name          string   `json:"name"`
	Installed     []string `json:"installed"`     // 清单里有、本机已装
	Missing       []string `json:"missing"`       // 清单里有、本机没装
	Extra         []string `json:"extra"`         // 本机已装但清单里没有
	ActiveWanted  string   `json:"activeWanted"`  // 清单要求的激活版本
	ActiveCurrent string   `json:"activeCurrent"` // 本机当前激活版本
	ActiveChanged bool     `json:"activeChanged"` // 激活版本不一致（且清单要求的版本本机已装）
}

// ImportReport 导入比对报告。
type ImportReport struct {
	Manifest     Manifest     `json:"manifest"`
	Runtimes     []ImportDiff `json:"runtimes"`
	MissingTotal int          `json:"missingTotal"`
	MatchedTotal int          `json:"matchedTotal"`
}

// CompareManifest 把清单与本机现状逐运行时比对。
// list 为 Manager.List() 的结果（含已装版本与激活状态）。
func CompareManifest(data []byte, list []RuntimeInfo) (ImportReport, error) {
	m, err := ParseManifest(data)
	if err != nil {
		return ImportReport{}, err
	}
	byID := make(map[string]RuntimeInfo, len(list))
	for _, info := range list {
		byID[info.ID] = info
	}

	rep := ImportReport{Manifest: m, Runtimes: make([]ImportDiff, 0, len(m.Runtimes))}
	for _, mr := range m.Runtimes {
		diff := ImportDiff{Runtime: mr.ID, Name: mr.Name, Installed: []string{}, Missing: []string{}, Extra: []string{}}
		info, ok := byID[mr.ID]
		have := map[string]bool{}
		if ok {
			for _, ins := range info.Installed {
				have[ins.Version] = true
				if ins.Active {
					diff.ActiveCurrent = ins.Version
				}
			}
		}
		want := map[string]bool{}
		for _, mv := range mr.Versions {
			want[mv.Version] = true
			if mv.Active {
				diff.ActiveWanted = mv.Version
			}
			if have[mv.Version] {
				diff.Installed = append(diff.Installed, mv.Version)
				rep.MatchedTotal++
			} else {
				diff.Missing = append(diff.Missing, mv.Version)
				rep.MissingTotal++
			}
		}
		if ok {
			for _, ins := range info.Installed {
				if !want[ins.Version] {
					diff.Extra = append(diff.Extra, ins.Version)
				}
			}
		}
		diff.ActiveChanged = diff.ActiveWanted != "" &&
			diff.ActiveWanted != diff.ActiveCurrent && have[diff.ActiveWanted]
		rep.Runtimes = append(rep.Runtimes, diff)
	}
	return rep, nil
}

// ApplyManifestActive 按清单把「本机已装且清单要求激活」的版本设为激活版本。
// 返回实际切换成功的条目描述；未安装的版本一律跳过（不自动下载）。
func (m *Manager) ApplyManifestActive(data []byte) ([]string, error) {
	rep, err := CompareManifest(data, m.List())
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, d := range rep.Runtimes {
		if !d.ActiveChanged {
			continue
		}
		if err := m.SetActive(Runtime(d.Runtime), d.ActiveWanted); err != nil {
			continue // 单个失败不影响其它运行时
		}
		applied = append(applied, fmt.Sprintf("%s %s", d.Name, d.ActiveWanted))
	}
	return applied, nil
}

// MergeManifest 把清单里的别名/备注合并到本机同名版本上（导入时可选）。
// 只补空值，不覆盖用户已写的别名/备注。
func MergeManifestNotes(m *Manager, data []byte) []string {
	mp, err := ParseManifest(data)
	if err != nil {
		return nil
	}
	var applied []string
	for _, mr := range mp.Runtimes {
		for _, mv := range mr.Versions {
			if mv.Alias == "" && mv.Note == "" {
				continue
			}
			installs, err := m.InstalledVersions(Runtime(mr.ID))
			if err != nil {
				continue
			}
			for _, ins := range installs {
				if ins.Version != mv.Version {
					continue
				}
				alias, note := ins.Alias, ins.Note
				if alias == "" {
					alias = mv.Alias
				}
				if note == "" {
					note = mv.Note
				}
				if alias == ins.Alias && note == ins.Note {
					break
				}
				if err := m.SetVersionMeta(Runtime(mr.ID), mv.Version, alias, note); err == nil {
					applied = append(applied, fmt.Sprintf("%s %s", mr.Name, mv.Version))
				}
				break
			}
		}
	}
	return applied
}

// ManifestString 序列化为带缩进的 JSON（前端直接下载为文件）。
func ManifestString(m Manifest) string {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m)
	return sb.String()
}
