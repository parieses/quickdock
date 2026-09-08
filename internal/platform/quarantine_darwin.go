//go:build darwin

package platform

import (
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/sysutil"
)

// ClearSelfQuarantine 在应用启动时（仅 darwin）自检自身所在 .app bundle 是否带
// com.apple.quarantine 隔离属性。带该属性时双击会被 Gatekeeper 拦截（"无法验证开发者"），
// 但我们的自动更新链路是 Go net/http 下载 + 自解压、不会写入该属性，因此只需在首次安装后清除一次。
//
// 说明：无 Apple 开发者账号时为 ad-hoc 签名，quarantine 清除后本机/信任设备即可正常运行；
// 正式分发仍需 Developer ID 签名 + 公证（见 docs/mac-update-sparkle-analysis.md）。
func ClearSelfQuarantine() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// 解析符号链接，得到真实路径（mac 上 .app 内的可执行体常被 ln 指向 Contents/MacOS）。
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	parts := strings.Split(filepath.Clean(exe), string(os.PathSeparator))
	var bundle string
	for i, p := range parts {
		if strings.HasSuffix(p, ".app") {
			bundle = string(os.PathSeparator) + filepath.Join(parts[1:i+1]...)
			break
		}
	}
	if bundle == "" {
		// 非 .app 内运行（如 go run / 裸二进制），无 quarantine 含义，跳过。
		return
	}
	// 直接 -dr 即可：属性不存在时 xattr 返回非 0，忽略即可。
	cmd := sysutil.Command("xattr", "-dr", "com.apple.quarantine", bundle)
	if err := cmd.Run(); err != nil {
		// 多半是属性本就不存在，属正常情况，仅记录。
		logger.W("[platform] 清除隔离属性时忽略（可能本就不存在）: %v", err)
		return
	}
	logger.I("[platform] 已清除 %s 的隔离属性，可正常打开", bundle)
}
