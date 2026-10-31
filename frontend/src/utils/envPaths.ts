// bin 目录推导：安装记录里 system 作用域的 `path` 指向可执行文件本身
// （.../bin/xxx.exe），而写 PATH 需要的是它所在的目录，故去掉最后一段。
// 其它作用域（managed / linked / portable）的 `path` 本身就是目录，原样返回。
//
// 单独成文件是因为「PATH 面板」与「Go 环境配置弹窗」都要用，
// 而它们分属两个 composable —— 不该为了 8 行函数让一个 import 另一个。
export function binDirOf(ins: { scope?: string; path?: string }): string {
  if (ins.scope === 'system') {
    const parts = (ins.path || '').split(/[\\/]/)
    parts.pop()
    return parts.join('/')
  }
  return ins.path || ''
}
