/**
 * QuickDock Goja 插件模板
 *
 * 运行在 QuickDock 内嵌的 JS 引擎（goja）中，零外部依赖。
 *
 * 必须导出的函数(固定名称)：
 *   handleInitialize(params)  → 插件初始化，返回 { status: 'ready' }
 *   handleExecute(params)     → 处理命令执行，params = { command, input }
 *
 * Goja API 可用（由 QuickDock 宿主注入）：
 *   api.log(msg)          — 写日志到后端
 *   api.readFile(path)    — 读取文件内容 (需要 filesystem 权限)
 *   api.writeFile(path, data) — 写入文件 (需要 filesystem 权限)
 *   api.httpGet(url)      — HTTP GET 请求 (需要 network 权限)
 *   api.httpPost(url, body)  — HTTP POST 请求 (需要 network 权限)
 *   api.db                — 插件专属 SQLite 数据库
 */

function handleInitialize(params) {
  api.log('插件初始化完成')
  return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  var input = params.input || {}
  var text = input.text || ''

  switch (command) {
    case 'hello':
      var name = text || 'World'
      return { text: 'Hello, ' + name + '!', display: 'Hello, ' + name + '!' }

    default:
      return { error: '未知命令: ' + command }
  }
}
