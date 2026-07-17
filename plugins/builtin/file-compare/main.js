/**
 * 文件/图片对比 — Goja 后端
 * 大部分逻辑在前端（File API + 拖拽），后端仅占位
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var command = params.command || ''
  if (command === 'open-file-compare') {
    return { status: 'ok', frontendOnly: true }
  }
  return { error: '未知命令' }
}
