/**
 * 文本编码/加密 — Goja 后端
 * Base64 / URL / HTML 编解码 + MD5 / SHA256 哈希
 * 所有算法纯 JS 实现，无外部依赖
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.1.0' }
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
      result = b64Encode(text)
      label = 'Base64 编码'
      break
    case 'base64-decode':
      result = b64Decode(text)
      label = 'Base64 解码'
      break
    case 'url-encode':
      result = urlEncode(text)
      label = 'URL 编码'
      break
    case 'url-decode':
      result = urlDecode(text)
      label = 'URL 解码'
      break
    case 'html-encode':
      result = htmlEncode(text)
      label = 'HTML 编码'
      break
    case 'html-decode':
      result = htmlDecode(text)
      label = 'HTML 解码'
      break
    case 'md5-hash':
      result = md5(text)
      label = 'MD5 哈希'
      break
    case 'sha256-hash':
      result = sha256(text)
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

// ========== Base64 ==========

var B64_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

function b64Encode(str) {
  var bytes = []
  for (var i = 0; i < str.length; i++) {
    var c = str.charCodeAt(i)
    if (c < 128) {
      bytes.push(c)
    } else if (c < 2048) {
      bytes.push(192 | (c >> 6))
      bytes.push(128 | (c & 63))
    } else {
      bytes.push(224 | (c >> 12))
      bytes.push(128 | ((c >> 6) & 63))
      bytes.push(128 | (c & 63))
    }
  }

  var result = ''
  for (var j = 0; j < bytes.length; j += 3) {
    var b0 = bytes[j]
    var b1 = j + 1 < bytes.length ? bytes[j + 1] : 0
    var b2 = j + 2 < bytes.length ? bytes[j + 2] : 0

    result += B64_ALPHABET.charAt(b0 >> 2)
    result += B64_ALPHABET.charAt(((b0 & 3) << 4) | (b1 >> 4))
    result += j + 1 < bytes.length ? B64_ALPHABET.charAt(((b1 & 15) << 2) | (b2 >> 6)) : '='
    result += j + 2 < bytes.length ? B64_ALPHABET.charAt(b2 & 63) : '='
  }

  return result
}

function b64Decode(str) {
  str = str.replace(/[^A-Za-z0-9+/=]/g, '')
  if (str.length % 4 !== 0) return '错误: Base64 字符串长度无效'

  var bytes = []
  for (var i = 0; i < str.length; i += 4) {
    var c0 = B64_ALPHABET.indexOf(str[i])
    var c1 = B64_ALPHABET.indexOf(str[i + 1])
    var c2 = B64_ALPHABET.indexOf(str[i + 2])
    var c3 = B64_ALPHABET.indexOf(str[i + 3])

    if (c0 < 0 || c1 < 0 || c2 < 0 || c3 < 0) return '错误: 包含无效 Base64 字符'

    bytes.push((c0 << 2) | (c1 >> 4))
    if (str[i + 2] !== '=') bytes.push(((c1 & 15) << 4) | (c2 >> 2))
    if (str[i + 3] !== '=') bytes.push(((c2 & 3) << 6) | c3)
  }

  // 将字节数组转换为 UTF-8 字符串
  return bytesToUTF8(bytes)
}

function bytesToUTF8(bytes) {
  var result = ''
  var i = 0
  while (i < bytes.length) {
    var b = bytes[i]
    if (b < 128) {
      result += String.fromCharCode(b)
      i++
    } else if (b < 224) {
      var c = ((b & 31) << 6) | (bytes[i + 1] & 63)
      result += String.fromCharCode(c)
      i += 2
    } else {
      var c2 = ((b & 15) << 12) | ((bytes[i + 1] & 63) << 6) | (bytes[i + 2] & 63)
      result += String.fromCharCode(c2)
      i += 3
    }
  }
  return result
}

// ========== URL 编码 ==========

function urlEncode(str) {
  var hex = '0123456789ABCDEF'
  var result = ''
  for (var i = 0; i < str.length; i++) {
    var c = str.charCodeAt(i)
    if (c >= 65 && c <= 90 || c >= 97 && c <= 122 || c >= 48 && c <= 57 ||
        c === 45 || c === 46 || c === 95 || c === 126) {
      result += str[i]
    } else if (c < 128) {
      result += '%' + hex.charAt(c >> 4) + hex.charAt(c & 15)
    } else if (c < 2048) {
      result += '%' + hex.charAt(192 >> 4 & 15) + hex.charAt(192 & 15)
      result += '%' + hex.charAt(128 | (c >> 6 & 3)) + hex.charAt(128 | (c & 63))
    } else {
      result += '%' + hex.charAt(224 >> 4 & 15) + hex.charAt(224 & 15)
      result += '%' + hex.charAt(128 | (c >> 6 & 3)) + hex.charAt(128 | (c & 63))
      result += '%' + hex.charAt(128 | (c >> 6 & 3)) + hex.charAt(128 | (c & 63))
    }
  }
  return result
}

function urlDecode(str) {
  var result = ''
  var i = 0
  while (i < str.length) {
    if (str[i] === '%' && i + 2 < str.length) {
      var code = parseInt(str.substring(i + 1, i + 3), 16)
      if (!isNaN(code)) {
        result += String.fromCharCode(code)
        i += 3
        continue
      }
    }
    if (str[i] === '+') {
      result += ' '
    } else {
      result += str[i]
    }
    i++
  }
  return result
}

// ========== HTML 编码 ==========

function htmlEncode(str) {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function htmlDecode(str) {
  return str
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&#x27;/g, "'")
    .replace(/&#x2F;/g, '/')
}

// ========== MD5（纯 JS 实现）==========

var MD5_S = [
  7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22,
  5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20,
  4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23,
  6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21
]

var MD5_K = [
  0xd76aa478, 0xe8c7b756, 0x242070db, 0xc1bdceee, 0xf57c0faf, 0x4787c62a, 0xa8304613, 0xfd469501,
  0x698098d8, 0x8b44f7af, 0xffff5bb1, 0x895cd7be, 0x6b901122, 0xfd987193, 0xa679438e, 0x49b40821,
  0xf61e2562, 0xc040b340, 0x265e5a51, 0xe9b6c7aa, 0xd62f105d, 0x02441453, 0xd8a1e681, 0xe7d3fbc8,
  0x21e1cde6, 0xc33707d6, 0xf4d50d87, 0x455a14ed, 0xa9e3e905, 0xfcefa3f8, 0x676f02d9, 0x8d2a4c8a,
  0xfffa3942, 0x8771f681, 0x6d9d6122, 0xfde5380c, 0xa4beea44, 0x4bdecfa9, 0xf6bb4b60, 0xbebfbc70,
  0x289b7ec6, 0xeaa127fa, 0xd4ef3085, 0x04881d05, 0xd9d4d039, 0xe6db99e5, 0x1fa27cf8, 0xc4ac5665,
  0xf4292244, 0x432aff97, 0xab9423a7, 0xfc93a039, 0x655b59c3, 0x8f0ccc92, 0xffeff47d, 0x85845dd1,
  0x6fa87e4f, 0xfe2ce6e0, 0xa3014314, 0x4e0811a1, 0xf7537e82, 0xbd3af235, 0x2ad7d2bb, 0xeb86d391
]

function md5(str) {
  var bytes = stringToBytes(str)
  // 附加 0x80
  var bitLen = bytes.length * 8
  bytes.push(0x80)
  // 填充到 (n * 512 - 64) bits
  while ((bytes.length * 8) % 512 !== 448) bytes.push(0)
  // 追加长度
  for (var i = 0; i < 8; i++) bytes.push((bitLen >>> (i * 8)) & 0xff)

  var h0 = 0x67452301, h1 = 0xefcdab89, h2 = 0x98badcfe, h3 = 0x10325476

  for (var block = 0; block < bytes.length; block += 64) {
    var w = new Array(16)
    for (var j = 0; j < 16; j++) {
      w[j] = bytes[block + j * 4] |
             (bytes[block + j * 4 + 1] << 8) |
             (bytes[block + j * 4 + 2] << 16) |
             (bytes[block + j * 4 + 3] << 24)
    }

    var a = h0, b = h1, c = h2, d = h3

    for (var k = 0; k < 64; k++) {
      var f, g
      if (k < 16) { f = (b & c) | (~b & d); g = k }
      else if (k < 32) { f = (d & b) | (~d & c); g = (5 * k + 1) % 16 }
      else if (k < 48) { f = b ^ c ^ d; g = (3 * k + 5) % 16 }
      else { f = c ^ (b | ~d); g = (7 * k) % 16 }

      f = (f + a + MD5_K[k] + w[g]) >>> 0
      var temp = d
      d = c
      c = b
      b = (b + ((f << MD5_S[k]) | (f >>> (32 - MD5_S[k])))) >>> 0
      a = temp
    }

    h0 = (h0 + a) >>> 0
    h1 = (h1 + b) >>> 0
    h2 = (h2 + c) >>> 0
    h3 = (h3 + d) >>> 0
  }

  return hex32(h0) + hex32(h1) + hex32(h2) + hex32(h3)
}

function stringToBytes(str) {
  var bytes = []
  for (var i = 0; i < str.length; i++) {
    var c = str.charCodeAt(i)
    if (c < 128) {
      bytes.push(c)
    } else if (c < 2048) {
      bytes.push(192 | (c >> 6), 128 | (c & 63))
    } else {
      bytes.push(224 | (c >> 12), 128 | ((c >> 6) & 63), 128 | (c & 63))
    }
  }
  return bytes
}

function hex32(n) {
  var s = (n >>> 0).toString(16)
  while (s.length < 8) s = '0' + s
  return s
}

// ========== SHA256（纯 JS 实现）==========

var SHA256_K = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
  0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
  0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
  0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
  0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
  0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
  0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
  0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
  0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
]

function sha256(str) {
  var bytes = stringToBytes(str)
  var bitLen = bytes.length * 8
  bytes.push(0x80)
  while ((bytes.length * 8) % 512 !== 448) bytes.push(0)
  for (var i = 0; i < 8; i++) bytes.push((bitLen >>> (56 - i * 8)) & 0xff)

  var H = [
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
  ]

  for (var block = 0; block < bytes.length; block += 64) {
    var w = new Array(64)
    for (var t = 0; t < 16; t++) {
      w[t] = bytes[block + t * 4] << 24 |
             bytes[block + t * 4 + 1] << 16 |
             bytes[block + t * 4 + 2] << 8 |
             bytes[block + t * 4 + 3]
    }
    for (var t2 = 16; t2 < 64; t2++) {
      w[t2] = (SHA256_sigma1(w[t2 - 2]) + w[t2 - 7] + SHA256_sigma0(w[t2 - 15]) + w[t2 - 16]) >>> 0
    }

    var a = H[0], b = H[1], c = H[2], d = H[3]
    var e = H[4], f = H[5], g = H[6], h = H[7]

    for (var t3 = 0; t3 < 64; t3++) {
      var S1 = SHA256_Σ1(e)
      var ch = (e & f) ^ (~e & g)
      var temp1 = (h + S1 + ch + SHA256_K[t3] + w[t3]) >>> 0
      var S0 = SHA256_Σ0(a)
      var maj = (a & b) ^ (a & c) ^ (b & c)
      var temp2 = (S0 + maj) >>> 0

      h = g; g = f; f = e
      e = (d + temp1) >>> 0
      d = c; c = b; b = a
      a = (temp1 + temp2) >>> 0
    }

    H[0] = (H[0] + a) >>> 0
    H[1] = (H[1] + b) >>> 0
    H[2] = (H[2] + c) >>> 0
    H[3] = (H[3] + d) >>> 0
    H[4] = (H[4] + e) >>> 0
    H[5] = (H[5] + f) >>> 0
    H[6] = (H[6] + g) >>> 0
    H[7] = (H[7] + h) >>> 0
  }

  return hex32(H[0]) + hex32(H[1]) + hex32(H[2]) + hex32(H[3]) +
         hex32(H[4]) + hex32(H[5]) + hex32(H[6]) + hex32(H[7])
}

function SHA256_Σ0(x) {
  return (((x >>> 2) | (x << 30)) ^ ((x >>> 13) | (x << 19)) ^ ((x >>> 22) | (x << 10))) >>> 0
}

function SHA256_Σ1(x) {
  return (((x >>> 6) | (x << 26)) ^ ((x >>> 11) | (x << 21)) ^ ((x >>> 25) | (x << 7))) >>> 0
}

function SHA256_sigma0(x) {
  return (((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3)) >>> 0
}

function SHA256_sigma1(x) {
  return (((x >>> 17) | (x << 15)) ^ ((x >>> 19) | (x << 13)) ^ (x >>> 10)) >>> 0
}

function RR(x, b) {
  return ((x >>> b) | (x << (32 - b))) >>> 0
}
