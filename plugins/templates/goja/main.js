/**
 * QuickDock Goja 插件示例
 * 运行在 QuickDock 内嵌的 JS 引擎中，无需安装 Node/Python
 *
 * 导出函数:
 *   handleInitialize(params) → 返回初始化结果
 *   handleExecute(params)    → 处理命令执行，params = { command, input }
 */

function handleInitialize(params) {
    api.log('插件初始化完成')
    return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
    var command = params.command || ''
    var input = params.input || {}

    if (command === 'hello') {
        var name = input.text || 'World'
        return { result: 'Hello, ' + name + '!' }
    }

    throw new Error('未知命令: ' + command)
}
