package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ---- 进程结束权限（目标进程白名单）----
//
// permissions.processKill 支持两种写法：
//
//	"processKill": true                    允许结束任意非系统关键进程
//	"processKill": ["chrome", "code.exe"]  仅允许结束白名单内的进程
//
// 为什么需要白名单：
// sysutil.KillProcess 已经挡住了 OS 关键进程（system / csrss / svchost / explorer…），
// 但那只是「不会把系统搞崩」的底线。声明了 processKill: true 的插件依然可以
// 结束用户正在用的浏览器、编辑器、微信——对用户来说这同样是不可接受的破坏。
// 白名单把「允许砍谁」的决定权交还给插件清单，与 process 的程序名白名单同构。
//
// 兼容性：历史上 processKill 是裸 bool。本结构用 UnmarshalJSON 同时接受
// bool 与数组两种写法，旧清单无需改动；空数组等价于 false（未声明任何目标）。
//
// 与 process 权限的区别（详见 procperm.go）：
//   - process     是「启动」新进程，程序名白名单约束将要跑的程序；
//   - processKill 是「结束」已有进程，白名单约束将要被砍的进程。
// 两者互不替代，需分别声明。

// ProcessKillPerm 进程结束权限
type ProcessKillPerm struct {
	All      bool     // 全放行（processKill: true）
	Programs []string // 目标进程白名单（processKill: [...]）
}

// UnmarshalJSON 兼容 bool 与数组两种写法。
//
// 注意空字符串 `""`：它既不等于 false 也不等于 true，落进数组分支会报
// 「cannot unmarshal string」——错误信息对不上用户实际写错的东西。
// 这里显式把它归为「未声明」，与 ProcessPerm 的疏漏区别开（那边同样有此问题，
// 由调用方 Validate 兜底，但错误信息更难懂）。
func (p *ProcessKillPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false", `""`:
		*p = ProcessKillPerm{}
		return nil
	case "true":
		*p = ProcessKillPerm{All: true}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf(`permissions.processKill 必须是 true/false 或 ["chrome", "code"]: %w`, err)
	}
	*p = ProcessKillPerm{Programs: arr}
	return nil
}

// MarshalJSON 与输入对称：全放行写回 true。
func (p ProcessKillPerm) MarshalJSON() ([]byte, error) {
	if p.All {
		return json.Marshal(true)
	}
	if p.Programs == nil {
		// nil 切片会序列化成 null，与输入的 false 不对称；空数组才是「未声明任何目标」。
		return json.Marshal(false)
	}
	return json.Marshal(p.Programs)
}

// Granted 是否声明了任何结束进程的能力
func (p ProcessKillPerm) Granted() bool {
	return p.All || len(p.Programs) > 0
}

// Validate 清单加载时的形式校验：白名单条目必须非空。
func (p ProcessKillPerm) Validate() error {
	if p.All {
		return nil
	}
	for _, prog := range p.Programs {
		if strings.TrimSpace(prog) == "" {
			return fmt.Errorf("%w: permissions.processKill 存在空条目", ErrInvalidManifest)
		}
	}
	return nil
}

// AllowsTarget 判断目标进程名是否被允许。
//
// 匹配规则与 ProcessPerm.AllowsProgram 基本一致，避免两处口径漂移：
//   - 全放行直接 true；
//   - 纯程序名 → 与目标相同即允许（.exe 后缀归一化，大小写不敏感）；
//   - 条目含路径分隔符 → 只取 basename 比对（见下方说明）；
//   - 不支持通配：一旦支持 "*.exe"，白名单就等于失效。
//
// 为什么路径形式也只比 basename：
// 这里的目标不是「即将执行的程序路径」，而是从 tasklist 拿到的**进程镜像名**
// （如 "chrome.exe"），拿不到完整路径。因此绝对路径条目无法做精确比对，
// 只保留「写完整路径的人本意是限定那个程序」这一语义，退化为 basename 匹配。
// 需要真正的路径级限制，应改用 process 权限（那边比对的是 exec.Command 的实参）。
//
// 空串目标（进程已退出 / 查不到名字）时：
//   - 全放行 → true（由 sysutil.KillProcess 的 PID 边界兜底）；
//   - 有白名单 → false。解析不出名字就无法证明它清白，宁可拒。
func (p ProcessKillPerm) AllowsTarget(name string) bool {
	if p.All {
		return true
	}
	if len(p.Programs) == 0 {
		return false
	}
	target := normalizeProgName(name)
	if target == "" {
		return false
	}
	for _, raw := range p.Programs {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		if strings.ContainsAny(rule, `/\`) {
			base := normalizeProgName(filepath.Base(rule))
			if base == target {
				return true
			}
			continue
		}
		if normalizeProgName(rule) == target {
			return true
		}
	}
	return false
}
