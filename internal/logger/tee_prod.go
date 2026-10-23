//go:build production

package logger

// teeStderr 正式版不写 stderr。
//
// 正式版用 `-H windowsgui` 构建，进程没有控制台，os.Stderr 是个无效句柄，
// 每次写入都是白做一次系统调用；而这里的调用点在所有日志路径上（含插件
// 逐行转发子进程输出的热路径）。
//
// 正式版的失败呈现交给 sysutil.ShowFatalError（原生弹窗），见 main.go。
const teeStderr = false
