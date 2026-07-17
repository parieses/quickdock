/**
 * 文本编码/加密 — Goja 后端
 * 所有算法（Base64 / URL / HTML 编解码 + MD5 / SHA256 哈希）均委托
 * api.crypto，由 Go 标准库实现，保证 UTF-8 / 多字节 / 4 字节代理对的正确性。
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入要处理的文本' }

  var command = params.command || ''
  var result
  var label

  switch (command) {
    case 'base64-encode':
      result = api.crypto.base64Encode(text)
      label = 'Base64 编码'
      break
    case 'base64-decode':
      // 解码失败时 api.crypto.base64Decode 会抛出，由调用方捕获为 error
      result = api.crypto.base64Decode(text)
      label = 'Base64 解码'
      break
    case 'url-encode':
      result = api.crypto.urlEncode(text)
      label = 'URL 编码'
      break
    case 'url-decode':
      result = api.crypto.urlDecode(text)
      label = 'URL 解码'
      break
    case 'html-encode':
      result = api.crypto.htmlEncode(text)
      label = 'HTML 编码'
      break
    case 'html-decode':
      result = api.crypto.htmlDecode(text)
      label = 'HTML 解码'
      break
    case 'md5-hash':
      result = api.crypto.md5(text)
      label = 'MD5 哈希'
      break
    case 'sha256-hash':
      result = api.crypto.sha256(text)
      label = 'SHA256 哈希'
      break
    default:
      return { error: '未知命令: ' + command }
  }

  return {
    text: result,
    display: '// ' + label + '  |  输入: ' + text.substring(0, 60) + (text.length > 60 ? '...' : '') + '\n────────────────────────────────────────\n' + result
  }
}
