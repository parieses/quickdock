package logger

// RecoverPanic 恢复 goroutine panic 防止整个应用崩溃
func RecoverPanic(context string) {
	if r := recover(); r != nil {
		E("QuickDock: [PANIC] %s: %v", context, r)
	}
}
