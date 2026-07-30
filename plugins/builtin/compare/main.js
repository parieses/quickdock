/**
 * 对比工具 — Goja 后端
 * 文件元数据对比、图片预览、文本内容 Diff 与文本块逐行 Diff 全部由前端实现，
 * 后端仅保留生命周期桩函数以满足插件加载要求。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  return { error: '该命令需通过前端界面使用: ' + command }
}
