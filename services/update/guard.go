package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 就地替换更新的"生效校验"守卫。
//
// 背景（v0.4.4 踩坑）：若发布包里注入的版本号（main.appVersion）低于 manifest 声明的版本
// ——典型原因是同一 commit 上打了多个 tag，构建用 git describe 取到旧 tag（v0.4.3/v0.4.4
// 同 commit 时返回 v0.4.3）——客户端就会陷入死循环：
//
//	检测到 0.4.4 > 当前 0.4.3 → 下载 → 就地替换 → 重启后仍是 0.4.3 → 又检测到 0.4.4 …
//
// 对策：替换前把目标版本记进 ~/.quickdock/update-guard.json，下次启动核对。
// 若连续 maxPendingAttempts 次替换后当前版本仍低于目标，就把该版本列入 skip，
// runCheck 不再提示它（CI 侧已改为用触发 tag 显式注入版本号，见 release.yml）。

const maxPendingAttempts = 2

// updateGuardState 持久化到 update-guard.json
type updateGuardState struct {
	PendingVersion string `json:"pendingVersion,omitempty"` // 已执行替换、待核对的目标版本
	Attempts       int    `json:"attempts,omitempty"`       // 同一目标版本的连续未生效次数
	SkippedVersion string `json:"skippedVersion,omitempty"` // 被判定"装不上"而忽略的版本
	SkippedFrom    string `json:"skippedFrom,omitempty"`    // 记录 skip 时的当前版本（版本一变就自动解封）
}

func guardPath() string {
	return filepath.Join(platform.DefaultDataDir(), "update-guard.json")
}

func loadGuard() updateGuardState {
	var s updateGuardState
	if data, err := os.ReadFile(guardPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func saveGuard(s updateGuardState) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(guardPath()), 0755)
	_ = os.WriteFile(guardPath(), data, 0644)
}

// MarkPendingUpdate 在触发就地替换前记录目标版本（同一版本累加尝试次数）。
func MarkPendingUpdate(version string) {
	if version == "" {
		return
	}
	s := loadGuard()
	if s.PendingVersion != version {
		s.PendingVersion = version
		s.Attempts = 0
	}
	s.Attempts++
	saveGuard(s)
	logger.I("[update] 记录待生效版本 %s（第 %d 次尝试）", version, s.Attempts)
}

// ClearPendingUpdate 替换实际没有启动（如 Updater.Restart 失败）时撤销记录。
func ClearPendingUpdate() {
	s := loadGuard()
	if s.PendingVersion == "" {
		return
	}
	s.PendingVersion, s.Attempts = "", 0
	saveGuard(s)
}

// ResolvePendingUpdate 启动时核对上次就地替换是否真的把版本提上去了：
//   - 当前版本 ≥ 目标：清除记录（更新成功）；
//   - 仍未提上去且未达阈值：保留记录，等下一次；
//   - 达到阈值：把该版本列入 skip，避免反复提示同一个装不上的版本。
func ResolvePendingUpdate(currentVersion string) {
	s := loadGuard()
	// 一旦应用版本发生变化（例如用安装器手动升级），之前的忽略记录自动解封
	if s.SkippedVersion != "" && s.SkippedFrom != currentVersion {
		logger.I("[update] 应用版本已变化（%s → %s），解除对 %s 的忽略", s.SkippedFrom, currentVersion, s.SkippedVersion)
		s.SkippedVersion, s.SkippedFrom = "", ""
		saveGuard(s)
	}
	if s.PendingVersion == "" {
		return
	}
	if versionAtLeast(currentVersion, s.PendingVersion) {
		logger.I("[update] 就地替换已生效：当前 %s ≥ 目标 %s", currentVersion, s.PendingVersion)
		s.PendingVersion, s.Attempts = "", 0
		saveGuard(s)
		return
	}
	logger.W("[update] 就地替换未生效：当前 %s 仍低于目标 %s（第 %d 次）。"+
		"通常是发布包注入的版本号低于 manifest 版本（同一 commit 重复打 tag 最容易触发）",
		currentVersion, s.PendingVersion, s.Attempts)
	if s.Attempts < maxPendingAttempts {
		return
	}
	s.SkippedVersion = s.PendingVersion
	s.SkippedFrom = currentVersion
	s.PendingVersion, s.Attempts = "", 0
	saveGuard(s)
	logger.W("[update] 已忽略版本 %s：连续 %d 次替换后版本号未变化（当前 %s）。"+
		"请发布修正了版本号注入的新版本；确需重试同一版本可删除 %s",
		s.SkippedVersion, maxPendingAttempts, currentVersion, guardPath())
}

// IsVersionSkipped 供 runCheck 过滤：命中被忽略的版本时按"已是最新"处理。
func IsVersionSkipped(version string) bool {
	if version == "" {
		return false
	}
	return loadGuard().SkippedVersion == version
}

// versionAtLeast 判断 a >= b（按 major.minor.patch 数值比较，忽略 pre-release 后缀）。
func versionAtLeast(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return true
}

// parseVersion 把 "v0.4.4-beta" 解析成 [0,4,4]，无法解析的段按 0 处理。
func parseVersion(v string) [3]int {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, r := range parts[i] {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out
}
