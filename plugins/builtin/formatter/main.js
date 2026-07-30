/**
 * 代码格式化 — Goja 后端
 * 通用代码（JS/CSS/HTML）压缩美化与 SQL 格式化全部由前端实现，
 * 后端仅保留生命周期桩函数以满足插件加载要求。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  return { error: '该命令需通过前端界面使用: ' + command }
}
