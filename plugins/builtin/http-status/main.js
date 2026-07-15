function handleInitialize(params) {
  return { status: 'ready', version: '1.0.0' }
}

var statusCodes = {
  // 1xx
  "100": { code: 100, label: "Continue", desc: "继续", category: "信息", detail: "服务器已收到请求头，客户端应继续发送请求体。" },
  "101": { code: 101, label: "Switching Protocols", desc: "切换协议", category: "信息", detail: "服务器已理解客户端的请求，将切换到更合适的协议。" },
  "102": { code: 102, label: "Processing", desc: "处理中", category: "信息", detail: "WebDAV 请求，服务器正在处理但尚无响应。" },
  "103": { code: 103, label: "Early Hints", desc: "早期提示", category: "信息", detail: "服务器在最终响应前提前返回一些响应头，主要用于预加载资源。" },
  // 2xx
  "200": { code: 200, label: "OK", desc: "成功", category: "成功", detail: "请求成功。GET 返回资源，POST 返回操作结果。" },
  "201": { code: 201, label: "Created", desc: "已创建", category: "成功", detail: "请求已被实现，新资源已创建。常用于 POST/PUT 响应。" },
  "202": { code: 202, label: "Accepted", desc: "已接受", category: "成功", detail: "请求已接受但尚未处理完成，用于异步操作。" },
  "204": { code: 204, label: "No Content", desc: "无内容", category: "成功", detail: "请求成功但无返回内容。常用于 DELETE 响应。" },
  // 3xx
  "301": { code: 301, label: "Moved Permanently", desc: "永久重定向", category: "重定向", detail: "请求的资源已被永久移动到新 URL，后续应使用新地址。" },
  "302": { code: 302, label: "Found", desc: "临时重定向", category: "重定向", detail: "请求的资源临时位于另一个 URL，后续仍用原地址。" },
  "304": { code: 304, label: "Not Modified", desc: "未修改", category: "重定向", detail: "资源未修改，客户端可使用缓存版本。用于条件请求。" },
  "307": { code: 307, label: "Temporary Redirect", desc: "临时重定向（保持请求方法）", category: "重定向", detail: "类似 302 但要求客户端保持原 HTTP 方法不变。" },
  "308": { code: 308, label: "Permanent Redirect", desc: "永久重定向（保持请求方法）", category: "重定向", detail: "类似 301 但要求客户端保持原 HTTP 方法不变。" },
  // 4xx
  "400": { code: 400, label: "Bad Request", desc: "错误请求", category: "客户端错误", detail: "服务器无法理解请求的格式，客户端不应不经修改重试。" },
  "401": { code: 401, label: "Unauthorized", desc: "未授权", category: "客户端错误", detail: "请求需要身份验证。客户端应提供有效的认证凭据。" },
  "403": { code: 403, label: "Forbidden", desc: "禁止访问", category: "客户端错误", detail: "服务器拒绝执行请求，即使有认证也无权限。" },
  "404": { code: 404, label: "Not Found", desc: "未找到", category: "客户端错误", detail: "服务器找不到请求的资源。可能是 URL 错误或资源已删除。" },
  "405": { code: 405, label: "Method Not Allowed", desc: "方法不允许", category: "客户端错误", detail: "请求方法不被该资源支持。例如不允许 DELETE。" },
  "408": { code: 408, label: "Request Timeout", desc: "请求超时", category: "客户端错误", detail: "服务器等待客户端发送请求时超时。" },
  "409": { code: 409, label: "Conflict", desc: "冲突", category: "客户端错误", detail: "请求与资源的当前状态冲突。常用于 PUT 版本冲突。" },
  "410": { code: 410, label: "Gone", desc: "已删除", category: "客户端错误", detail: "请求的资源已永久删除，与 404 不同，410 明确表示资源曾存在。" },
  "413": { code: 413, label: "Payload Too Large", desc: "请求实体过大", category: "客户端错误", detail: "请求体超过服务器允许的大小限制。" },
  "415": { code: 415, label: "Unsupported Media Type", desc: "不支持的媒体类型", category: "客户端错误", detail: "请求的格式不被请求的资源支持。" },
  "422": { code: 422, label: "Unprocessable Entity", desc: "不可处理的实体", category: "客户端错误", detail: "请求格式正确但语义错误。常用于表单验证失败。" },
  "429": { code: 429, label: "Too Many Requests", desc: "请求过多", category: "客户端错误", detail: "客户端在指定时间内发送了太多请求（限流）。" },
  // 5xx
  "500": { code: 500, label: "Internal Server Error", desc: "服务器内部错误", category: "服务器错误", detail: "服务器遇到意外错误，无法完成请求。最常见的 5xx 错误。" },
  "501": { code: 501, label: "Not Implemented", desc: "未实现", category: "服务器错误", detail: "服务器不支持请求的功能，无法处理请求。" },
  "502": { code: 502, label: "Bad Gateway", desc: "网关错误", category: "服务器错误", detail: "网关或代理从上游服务器收到无效响应。" },
  "503": { code: 503, label: "Service Unavailable", desc: "服务不可用", category: "服务器错误", detail: "服务器暂时无法处理请求（过载或维护中）。" },
  "504": { code: 504, label: "Gateway Timeout", desc: "网关超时", category: "服务器错误", detail: "网关或代理等待上游服务器响应超时。" },
  "505": { code: 505, label: "HTTP Version Not Supported", desc: "HTTP 版本不支持", category: "服务器错误", detail: "服务器不支持请求中使用的 HTTP 协议版本。" }
}

function handleExecute(params) {
  var input = params.input ? (params.input.text || '') : ''
  var text = input.trim()

  if (!text) return { error: '请输入 HTTP 状态码（如 404）' }

  // 提取数字
  var match = text.match(/(\d{3})/)
  if (!match) return { error: '未找到有效的 HTTP 状态码' }

  var code = match[1]
  var info = statusCodes[code]

  if (!info) return { error: '未知状态码 ' + code }

  var result = '[' + info.category + '] ' + info.code + ' ' + info.label + '\n'
  result += info.desc + '\n'
  result += '\n' + info.detail

  return {
    text: info.label + ' - ' + info.desc,
    display: result
  }
}
