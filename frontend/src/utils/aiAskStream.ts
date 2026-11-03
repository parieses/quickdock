/**
 * 面板类「问 AI」的本地流式通道（SSH / 剪贴板 / 笔记）。
 *
 * 为什么不继续用 Wails 事件（ai:ask-delta）：事件在绑定方法执行期间会被攒到
 * 方法返回后才投递，面板上看到的是「等半天、整段蹦出来」。AI 对话页早就为此
 * 改走 127.0.0.1 上的 SSE（见 services/ai/ai_stream.go），这里把同一套办法
 * 复用到面板上——后端端点见 services/ai/ai_ask_stream.go。
 *
 * 业务参数与结果都不在这里定义：body 由调用方按各自场景组装，结果原样
 * 反序列化成调用方给的类型（与原来绑定方法的返回值同构）。
 */
import { AIStreamInfo } from '../../bindings/quickdock/services/ai/aiservice'
import { unwrap } from './api'

export interface AiAskHandlers {
  /** 正文增量 */
  onDelta?: (text: string) => void
  /** 思考增量（模型开了 thinking 才有）。不传就等于不显示思考。 */
  onReason?: (text: string) => void
}

interface StreamInfo {
  port: number
  token: string
}

// 端口与令牌是进程级的，取一次就够。缓存起来也避免每个面板各打一次绑定调用。
let cached: StreamInfo | null = null

async function streamInfo(): Promise<StreamInfo> {
  if (cached) return cached
  const info = unwrap<StreamInfo>(await AIStreamInfo())
  if (!info || !info.port) throw new Error('流式服务不可用')
  cached = info
  return info
}

/** 解析一帧（`event: x` + `data: {...}`）。格式不认识就返回 null 跳过。 */
function parseFrame(frame: string): { event: string; data: Record<string, any> } | null {
  let event = ''
  const dataLines: string[] = []
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
  }
  if (!event || !dataLines.length) return null
  try {
    return { event, data: JSON.parse(dataLines.join('\n')) }
  } catch {
    return null
  }
}

/**
 * 发起一次面板问答，边收边回调，结束时返回结构化结果。
 *
 * 失败一律抛错：调用方原本就在 catch 里做 toast 与 errMsg，这样两条通道
 * （绑定方法 / SSE）的失败处理才能是同一段代码。
 */
export async function askOverStream<T>(kind: string, body: unknown, handlers: AiAskHandlers = {}): Promise<T> {
  const info = await streamInfo()
  const resp = await fetch(`http://127.0.0.1:${info.port}/ai/ask-stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Stream-Token': info.token },
    body: JSON.stringify({ kind, body }),
  })
  if (!resp.ok) {
    // 404 = 后端没注册这个场景（多见于只更新了前端、主程序还是旧版）。
    throw new Error(resp.status === 404 ? `未注册的问答场景：${kind}` : `HTTP ${resp.status}`)
  }
  if (!resp.body) throw new Error('流式响应不可读')

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let result: T | null = null
  let failure = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let cut = buf.indexOf('\n\n')
    while (cut >= 0) {
      const frame = parseFrame(buf.slice(0, cut))
      buf = buf.slice(cut + 2)
      cut = buf.indexOf('\n\n')
      if (!frame) continue
      switch (frame.event) {
        case 'delta':
          handlers.onDelta?.(String(frame.data.text ?? ''))
          break
        case 'reason':
          handlers.onReason?.(String(frame.data.text ?? ''))
          break
        case 'done':
          result = (frame.data.result ?? null) as T
          break
        case 'error':
          failure = String(frame.data.message ?? 'AI 调用失败')
          break
      }
    }
  }

  if (failure) throw new Error(failure)
  if (result === null) throw new Error('AI 没有返回结果')
  return result
}
