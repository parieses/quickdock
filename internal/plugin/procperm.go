package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ---- 进程执行权限（可执行程序白名单）----
//
// permissions.process 支持两种写法（向后兼容，缺省即无权限）：
//
//	"process": true                    允许执行任意程序（等价于不加白名单限制）
//	"process": ["git", "reg", "netstat"] 仅允许执行白名单内的程序
//
// 为什么用「程序名白名单」而不是路径 scope：
// 执行能力的风险不在于「读哪个文件」，而在于「跑哪个程序」。
// `netstat` / `tasklist` / `netsh` 这类系统诊断程序是低风险的，
// 而 `powershell` / `cmd` / `mshta` 可以在参数里塞任意脚本，
// 放行它们等于把整台机器交出去——所以白名单匹配的是**程序自身**，
// 且不区分路径：声明 "netstat" 允许系统 PATH 里的任意 netstat，
// 声明绝对路径 "C:\Windows\System32\netstat.exe" 则只允许那一个文件。
//
// 与 shell 权限的区别：
//   - shell 是「打开目标」（file:// / https://），不执行任何东西；
//   - process 是「执行程序」，会在宿主权限下真的起一个进程。
// 二者能力量级完全不同，不可互相替代。
//
// 与 processKill 的区别：
//   - processKill 是「结束」别人已经存在的进程（host.process.kill）；
//   - process 是「启动」新进程。两者都是高危操作，需分别声明。
//
// 语义边界（重要）：
// 本权限目前**只对宿主桥接方法 host.process.exec 有效**——
// 插件通过该方法执行程序时，宿主会在启动前校验程序名是否在白名单内。
// native 插件在自己的进程里直接调用 exec.Command，宿主无法在运行期拦截，
// 那一路只能靠安装期的静态审计（见 permcheck.go）来核对声明与实现是否相符。
// 因此「声明了 process」不等于「只能执行白名单内的程序」，
// 真正的强制隔离需要进程级沙箱，那是另一条路线。

// ProcessPerm 进程执行权限
type ProcessPerm struct {
	All      bool     // 全放行（process: true）
	Programs []string // 可执行程序白名单（process: [...]）
}

// UnmarshalJSON 兼容 bool 与数组两种写法。
func (p *ProcessPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false":
		*p = ProcessPerm{}
		return nil
	case "true":
		*p = ProcessPerm{All: true}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf(`permissions.process 必须是 true/false 或 ["git", "reg"]: %w`, err)
	}
	*p = ProcessPerm{Programs: arr}
	return nil
}

// MarshalJSON 与输入对称：全放行写回 true。
func (p ProcessPerm) MarshalJSON() ([]byte, error) {
	if p.All {
		return json.Marshal(true)
	}
	// nil 切片会序列化成 null，与输入的 false 不对称；空数组才是「未声明任何程序」。
	if p.Programs == nil {
		return json.Marshal(false)
	}
	return json.Marshal(p.Programs)
}

// Granted 是否声明了任何进程执行能力
func (p ProcessPerm) Granted() bool {
	return p.All || len(p.Programs) > 0
}

// Validate 清单加载时的形式校验：白名单条目必须非空。
func (p ProcessPerm) Validate() error {
	if p.All {
		return nil
	}
	for _, prog := range p.Programs {
		if strings.TrimSpace(prog) == "" {
			return fmt.Errorf("%w: permissions.process 存在空条目", ErrInvalidManifest)
		}
	}
	return nil
}

// AllowsProgram 判断待执行程序是否被允许。
//
// 匹配规则：
//   - 全放行直接 true；
//   - 白名单条目若含路径分隔符 → 按路径比较（绝对路径精确匹配，
//     相对路径则与目标做 basename 比较）；
//   - 白名单条目若只是程序名（如 "netstat"）→ 与目标的 basename 相同即允许，
//     不限来源目录，这样声明一次就能覆盖 PATH 里的系统工具；
//   - 允许通过 "*.exe" 之类的通配？—— 不允许。程序名白名单一旦支持通配，
//     "powershell" 就能被写成 "*.exe" 绕过，等于白名单失效。
func (p ProcessPerm) AllowsProgram(program string) bool {
	if p.All {
		return true
	}
	if len(p.Programs) == 0 {
		return false
	}
	target := strings.TrimSpace(program)
	if target == "" {
		return false
	}
	targetBase := normalizeProgName(filepath.Base(target))

	for _, raw := range p.Programs {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		if strings.ContainsAny(rule, `/\`) {
			// 路径形式：绝对路径要求完全一致（大小写不敏感）；
			// 相对路径只与目标的 basename 比较。
			if filepath.IsAbs(rule) {
				if strings.EqualFold(normalizeProgName(filepath.Clean(rule)), normalizeProgName(filepath.Clean(target))) {
					return true
				}
				continue
			}
			if normalizeProgName(rule) == targetBase {
				return true
			}
			continue
		}
		// 纯程序名形式：与目标 basename 比较。
		if normalizeProgName(rule) == targetBase {
			return true
		}
	}
	return false
}

// normalizeProgName 归一化程序名用于比对：
//   - 去掉 Windows 上常见的 .exe 后缀，让 "netstat" 与 "netstat.exe" 视为同一程序
//     （插件里写 exec.Command("netstat")，实际落地的却是 netstat.exe，
//     若不归一化，白名单写了 netstat 却拦下真实调用）；
//   - 统一小写，因为 Windows 文件系统大小写不敏感。
func normalizeProgName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".exe")
	return n
}
