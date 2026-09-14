package main

import (
	"os"

	"quickdock/internal/sites"
)

// runElevatedCLI 处理「以管理员身份自举」的子进程路径：这次进程只做一件事——
// 把站点域名写进系统 hosts 的标记区块，然后立即退出。
//
// 必须在 main 的最开头就返回：application.Options 里配了单实例互斥，
// 走到 application.New 的话，这个提权进程会被当成「重复启动」而把活转交给首实例并退出，
// hosts 就永远写不进去。
//
// 失败只体现在退出码上（父进程据此报错），这里刻意不初始化日志：
// 提权进程若新建/追加日志文件，日志的属主会变成管理员，之后普通权限的宿主写日志反而可能失败。
func runElevatedCLI() {
	if len(os.Args) < 2 || os.Args[1] != sites.ElevateHostsFlag {
		return
	}
	if err := sites.SyncHostsCLI(os.Args[2:]); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
