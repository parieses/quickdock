//go:build !production

package logger

// teeStderr dev 版把每条日志同时写一份到 stderr。
//
// 为什么按构建标签区分而不是运行时判断：dev 构建由 `wails3 dev` 拉起，
// 是有真实控制台的普通子系统程序，控制台就在用户眼前；正式版构建带
// `-H windowsgui`，进程根本没有控制台，写 stderr 是纯无效操作。
//
// 这条通道的价值在于「启动阶段就崩」的场景：日志文件虽然落了，但用户
// 得先知道去哪儿看。v0.0.25 的迁移失败就是这么被漏掉的——控制台一片
// 空白、窗口没起来，只有翻 log 才看到真相。
const teeStderr = true
