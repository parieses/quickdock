//go:build darwin || linux

package platform

import (
	"fmt"
	"runtime"

	"quickdock/internal/sysutil"
)

// RunSystemCommand 执行系统命令（非 Windows 实现）。
//
// 与 Windows 版保持相同的命令名契约，便于前端无分支调用：
//   - lock           锁屏
//   - sleep          休眠
//   - shutdown       关机
//   - restart        重启
//   - emptytrash     清空废纸篓
//   - kill-foreground 结束前台应用
//
// 以下为 Windows 专属语义，非 Windows 直接返回「不支持」：
//   - window-left / window-right（Win+方向键吸附，mac 需 Accessibility API 重写）
//   - volume-up / volume-down / volume-mute（mac 可用 osascript 调音量，见下）
//   - wifi-toggle（mac 需 CoreWLAN，暂不支持）
func RunSystemCommand(cmd string) error {
	switch cmd {
	case "lock":
		if runtime.GOOS == "darwin" {
			// CGSession -suspend 锁屏（不经屏幕保护）
			return run("CGSession", "-suspend")
		}
		return run("xdg-screensaver", "lock")
	case "sleep":
		if runtime.GOOS == "darwin" {
			// pmset sleepnow 立即休眠
			return run("pmset", "sleepnow")
		}
		return run("systemctl", "suspend")
	case "shutdown":
		if runtime.GOOS == "darwin" {
			return osa(`tell app "System Events" to shut down`)
		}
		return run("shutdown", "-h", "now")
	case "restart":
		if runtime.GOOS == "darwin" {
			return osa(`tell app "System Events" to restart`)
		}
		return run("reboot")
	case "emptytrash":
		if runtime.GOOS == "darwin" {
			return osa(`tell app "Finder" to empty the trash`)
		}
		return unsupported("emptytrash")
	case "volume-mute", "volume-up", "volume-down":
		if runtime.GOOS == "darwin" {
			return setVolume(cmd)
		}
		return unsupported(cmd)
	case "kill-foreground":
		// 结束最前台应用（不含 QuickDock 自身的防护由调用方保证）
		if runtime.GOOS == "darwin" {
			return osa(`tell app "System Events" to keystroke "q" using {command down}`)
		}
		return unsupported("kill-foreground")
	default:
		return unsupported(cmd)
	}
}

// setVolume 通过 osascript 调整系统音量。
// 静音为开关语义：当前已静音则取消，否则置为静音。
func setVolume(cmd string) error {
	if cmd == "volume-mute" {
		return osa(`tell application "Finder"
	if (get volume settings)'s output muted then
		set volume output muted false
	else
		set volume output muted true
	end if
end tell`)
	}
	delta := 7 // 音量档位步长
	if cmd == "volume-down" {
		delta = -7
	}
	return osa(fmt.Sprintf("set volume output volume (output volume of (get volume settings) + %d)", delta))
}

func unsupported(cmd string) error {
	return fmt.Errorf("该命令在当前平台不支持: %s", cmd)
}

func run(name string, args ...string) error {
	if err := sysutil.Command(name, args...).Run(); err != nil {
		return fmt.Errorf("%s 执行失败: %v", name, err)
	}
	return nil
}

func osa(script string) error {
	if err := sysutil.Command("osascript", "-e", script).Run(); err != nil {
		return fmt.Errorf("osascript 执行失败: %v", err)
	}
	return nil
}
