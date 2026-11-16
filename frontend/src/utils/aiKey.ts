/**
 * 「这个 AI 档案是否必须有 API Key」的唯一判定。
 *
 * 本地推理服务（Ollama、LM Studio、vLLM…）与内网免鉴权网关都不需要 Key。
 * 后端 services/ai 的 profileNeedsKey 用同一口径（provider=ollama 或回环 BaseURL）；
 * 前端这个判断决定输入框是否置灰、要不要弹「请配置 API Key」。两处一旦分叉，
 * 就会出现「设置里明明配好且能测通、对话页仍然不让发」这种谁也说不清的问题。
 *
 * 反过来不能放宽到任意地址：公网服务空 Key 只会拿到 401，
 * 提前提示「去配 Key」比让用户看一条鉴权失败更有用。
 */
export function isLocalAIEndpoint(baseURL: string): boolean {
  try {
    const host = new URL((baseURL || '').trim()).hostname.toLowerCase()
    return host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '[::1]'
  } catch {
    // 地址还没填完（如 "http://"）时不算本地，交给「必填 Base URL」那条校验。
    return false
  }
}

export function profileNeedsKey(p: { provider?: string; baseURL?: string } | null | undefined): boolean {
  if (!p) return true
  if ((p.provider || '').trim().toLowerCase() === 'ollama') return false
  return !isLocalAIEndpoint(p.baseURL || '')
}
