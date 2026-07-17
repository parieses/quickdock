function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

// 标准 Base64 URL 解码
function b64UrlDecode(str) {
  var s = str.replace(/-/g, '+').replace(/_/g, '/')
  while (s.length % 4) s += '='
  try {
    return decodeURIComponent(escape(atob(s)))
  } catch (e) {
    try { return atob(s) } catch (e2) { return '' }
  }
}

// 解析 JWT
function parseJWT(token) {
  var parts = token.split('.')
  if (parts.length !== 3) return { error: '无效的 JWT Token，需要 3 段（header.payload.signature）' }

  var headerJson = b64UrlDecode(parts[0])
  var payloadJson = b64UrlDecode(parts[1])

  var header, payload
  try { header = JSON.parse(headerJson) } catch (e) { header = { _raw: headerJson } }
  try { payload = JSON.parse(payloadJson) } catch (e) { payload = { _raw: payloadJson } }

  // 验证过期
  var info = {}
  if (payload.exp) {
    var expDate = new Date(payload.exp * 1000)
    var now = Date.now() / 1000
    info.expired = payload.exp < now
    info.expTime = expDate.toISOString().replace('T', ' ').substring(0, 19)
    info.remaining = info.expired
      ? '已过期 ' + Math.floor((now - payload.exp) / 60) + ' 分钟'
      : '剩余 ' + Math.floor((payload.exp - now) / 3600) + ' 小时 ' + Math.floor(((payload.exp - now) % 3600) / 60) + ' 分钟'
  }
  if (payload.iat) {
    info.iatTime = new Date(payload.iat * 1000).toISOString().replace('T', ' ').substring(0, 19)
  }
  if (payload.sub) info.subject = payload.sub
  if (payload.iss) info.issuer = payload.iss

  return {
    header: header,
    payload: payload,
    signature: parts[2].substring(0, 20) + '...',
    info: info,
    raw: { header: parts[0], payload: parts[1], signature: parts[2] }
  }
}

function handleExecute(params) {
  var input = params.input ? params.input.text || '' : ''
  var text = input || ''

  // 自动从输入中提取 JWT
  var match = text.match(/(eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/)
  if (match) text = match[1]

  if (!text || text.split('.').length !== 3) {
    return { text: '', error: '请输入有效的 JWT Token（以 eyJ 开头）' }
  }

  var result = parseJWT(text)
  if (result.error) {
    return { text: '', error: result.error }
  }

  return {
    text: JSON.stringify({ header: result.header, payload: result.payload }, null, 2),
    display: JSON.stringify({ header: result.header, payload: result.payload, info: result.info }, null, 2)
  }
}
