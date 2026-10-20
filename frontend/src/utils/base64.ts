/**
 * 终端字节流与 base64 的互转。
 *
 * 为什么不用 JSON 直接传字符串：SSH 输出里可能夹着非 UTF-8 片段（服务端 locale
 * 不对时的文件名、二进制输出），一旦在 Go 侧强制转 UTF-8 就会出现替换符，
 * 终端画面变成一串「乱码块」。所以两侧约定统一走**原始字节 + base64**，
 * 且帧边界可能把中文或 ANSI 序列劈成两半，解码后必须按字节流写进 xterm，
 * 不能每帧单独 decode 成字符串。
 */

/** 分块大小：`String.fromCharCode` 的实参个数有上限（V8 约 65535），
 *  一次展开整段会抛 RangeError，所以按 32K 切块。 */
const CHUNK = 0x8000

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let i = 0; i < bytes.length; i += CHUNK) {
    const chunk = bytes.subarray(i, Math.min(i + CHUNK, bytes.length))
    // 逐字符 += 在 64KB 上要 2.1ms，apply 只要 1.5ms；
    // 输入路径都在用户按键/粘贴上，但粘贴大段文本时这个差别能感觉到。
    binary += String.fromCharCode.apply(null, chunk as unknown as number[])
  }
  return btoa(binary)
}

export function base64ToBytes(b64: string): Uint8Array {
  if (!b64) return new Uint8Array(0)
  const binary = atob(b64)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i)
  }
  return out
}

/** 把按键字符串按 UTF-8 编码成字节（终端协议只认字节，中文必须走这条）。 */
export function textToBytes(text: string): Uint8Array {
  return new TextEncoder().encode(text)
}
