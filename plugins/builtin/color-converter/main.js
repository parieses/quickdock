/**
 * 颜色转换 — Goja 后端
 * 输入 HEX / RGB / HSL 或常见英文色名，输出三种格式互转结果。
 * 纯 JS 算法实现，无外部依赖。
 */

// 常见英文色名 → HEX（精简常用集）
var NAMED_COLORS = {
  red: '#ff0000', green: '#008000', blue: '#0000ff',
  black: '#000000', white: '#ffffff', gray: '#808080', grey: '#808080',
  yellow: '#ffff00', orange: '#ffa500', purple: '#800080',
  pink: '#ffc0cb', cyan: '#00ffff', magenta: '#ff00ff',
  brown: '#a52a2a', lime: '#00ff00', teal: '#008080',
  navy: '#000080', silver: '#c0c0c0', gold: '#ffd700',
  indigo: '#4b0082', violet: '#ee82ee', coral: '#ff7f50',
  crimson: '#dc143c', salmon: '#fa8072', turquoise: '#40e0d0',
  beige: '#f5f5dc', khaki: '#f0e68c', olive: '#808000'
}

function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)) }

function hexToRgb(hex) {
  hex = hex.replace('#', '')
  if (hex.length === 3) hex = hex.split('').map(function (c) { return c + c }).join('')
  if (hex.length === 8) hex = hex.slice(0, 6) // 忽略 alpha
  if (hex.length !== 6) return null
  var n = parseInt(hex, 16)
  if (isNaN(n)) return null
  return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 }
}

function rgbToHex(r, g, b) {
  function p(v) { var s = clamp(Math.round(v), 0, 255).toString(16); return s.length === 1 ? '0' + s : s }
  return '#' + p(r) + p(g) + p(b)
}

function rgbToHsl(r, g, b) {
  r /= 255; g /= 255; b /= 255
  var max = Math.max(r, g, b), min = Math.min(r, g, b)
  var h = 0, s = 0, l = (max + min) / 2
  if (max !== min) {
    var d = max - min
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
    if (max === r) h = (g - b) / d + (g < b ? 6 : 0)
    else if (max === g) h = (b - r) / d + 2
    else h = (r - g) / d + 4
    h *= 60
  }
  return { h: Math.round(h), s: Math.round(s * 100), l: Math.round(l * 100) }
}

function hslToRgb(h, s, l) {
  h = ((h % 360) + 360) % 360
  s = clamp(s, 0, 100) / 100
  l = clamp(l, 0, 100) / 100
  function f(n) {
    var k = (n + h / 30) % 12
    var a = s * Math.min(l, 1 - l)
    return l - a * Math.max(-1, Math.min(k - 3, Math.min(9 - k, 1)))
  }
  return { r: Math.round(f(0) * 255), g: Math.round(f(8) * 255), b: Math.round(f(4) * 255) }
}

// 解析任意输入为 {r,g,b}；无法识别返回 null
function parseColor(text) {
  text = text.trim()
  var m

  // 英文色名
  if (NAMED_COLORS[text.toLowerCase()]) {
    return hexToRgb(NAMED_COLORS[text.toLowerCase()])
  }
  // HEX：#fff / #ffffff / #ffffffff
  if (/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.test(text)) {
    return hexToRgb(text)
  }
  // rgb() / rgba()
  m = text.match(/^rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})(?:\s*,\s*[\d.]+)?\s*\)$/)
  if (m) {
    return { r: parseInt(m[1]), g: parseInt(m[2]), b: parseInt(m[3]) }
  }
  // hsl() / hsla()
  m = text.match(/^hsla?\(\s*(\d{1,3})\s*,\s*(\d{1,3})%\s*,\s*(\d{1,3})%(?:\s*,\s*[\d.]+)?\s*\)$/)
  if (m) {
    return hslToRgb(parseInt(m[1]), parseInt(m[2]), parseInt(m[3]))
  }
  return null
}

function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var text = (input.text || '').trim()
  if (!text) return { error: '请输入颜色，如 #ff0000、rgb(255,0,0)、hsl(0,100%,50%)、red' }

  var rgb = parseColor(text)
  if (!rgb) return { error: '无法识别的颜色格式: ' + text }

  var hex = rgbToHex(rgb.r, rgb.g, rgb.b)
  var hsl = rgbToHsl(rgb.r, rgb.g, rgb.b)
  var rgbStr = 'rgb(' + rgb.r + ', ' + rgb.g + ', ' + rgb.b + ')'
  var hslStr = 'hsl(' + hsl.h + ', ' + hsl.s + '%, ' + hsl.l + '%)'

  var display = '// 颜色转换 | 输入: ' + text + '\n'
  display += '────────────────────────────\n'
  display += 'HEX: ' + hex + '\n'
  display += 'RGB: ' + rgbStr + '\n'
  display += 'HSL: ' + hslStr + '\n'
  display += '预览: ' + hex

  return {
    text: hex + '\n' + rgbStr + '\n' + hslStr,
    display: display
  }
}
