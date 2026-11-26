// 剪贴板条目的「代码预览」：把 JSON / 代码类文本渲染成带语法高亮的只读块。
//
// 为什么不直接用 highlight.js/lib/common：ClipboardPanel 同时也是 Ctrl+~ 浮窗的
// 渲染入口（compact 模式），common 带 37 种语言、首次解析要几十毫秒，浮窗唤起的
// 第一帧会被它拖住。这里只注册剪贴板里真正会出现的几种语言，按需 highlightAuto
// 在这几个候选里猜，代价可控。
import hljs from 'highlight.js/lib/core'
import jsonLang from 'highlight.js/lib/languages/json'
import jsLang from 'highlight.js/lib/languages/javascript'
import tsLang from 'highlight.js/lib/languages/typescript'
import pyLang from 'highlight.js/lib/languages/python'
import goLang from 'highlight.js/lib/languages/go'
import phpLang from 'highlight.js/lib/languages/php'
import sqlLang from 'highlight.js/lib/languages/sql'
import bashLang from 'highlight.js/lib/languages/bash'
import { classifyTextType } from './clipboardFilter'

hljs.registerLanguage('json', jsonLang)
hljs.registerLanguage('javascript', jsLang)
hljs.registerLanguage('typescript', tsLang)
hljs.registerLanguage('python', pyLang)
hljs.registerLanguage('go', goLang)
hljs.registerLanguage('php', phpLang)
hljs.registerLanguage('sql', sqlLang)
hljs.registerLanguage('bash', bashLang)

/** highlightAuto 的候选集：只在这几种里打分，避免「逐个语言试探」把首屏拖慢。 */
const AUTO_LANGS = ['javascript', 'typescript', 'json', 'python', 'go', 'php', 'sql', 'bash']

/** 超过这个长度就不猜语言了：highlightAuto 要逐个语言打分，几千字的片段配上
 *  一页 20 条会把切页拖出可感知的掉帧。这些长片段仍按等宽文本展示，只是不上色。 */
const AUTO_MAX_CHARS = 1500
/** 超过这个长度整块都不高亮，按纯文本展示（贴一大段日志进来时很常见）。 */
const MAX_CHARS = 20000
const CACHE_MAX = 160

export interface ClipboardCodeView {
  /** 已转义 + 带 hljs-* span 的 HTML，可安全交给 v-html。 */
  html: string
  /** 识别出的语言；识别不出为 text。 */
  lang: string
}

// 键用条目 id：同一 id 的正文不会变（同内容重复复制走的是 copy_count 自增）。
// 缓存是为了让滚动 / 勾选 / 每 30s 轮询刷新这些重渲染不再重跑高亮。
const cache = new Map<string, ClipboardCodeView>()

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/**
 * 该条目是否按代码块渲染，返回可直接塞进 <pre> 的高亮结果；普通文本返回 null。
 *
 * @param textType 后端入库时算好的 text_type（老数据缺失时用 classifyTextType 兜底）
 */
export function clipboardCodeView(
  id: string,
  text: string,
  textType?: string,
): ClipboardCodeView | null {
  if (!text) return null
  const type = textType || classifyTextType(text)
  // 只认 json / code：url/email/phone 是短文本，套代码块反而更难看。
  if (type !== 'json' && type !== 'code') return null
  if (text.length > MAX_CHARS) return null

  const cached = cache.get(id)
  if (cached) return cached

  let source = text
  let lang = ''
  try {
    if (type === 'json') {
      // JSON 一律先美化：剪贴板里存的常是一行压缩 JSON，原样显示没法读。
      try {
        source = JSON.stringify(JSON.parse(text), null, 2)
        lang = 'json'
      } catch {
        // 后端按「首尾是大括号/方括号」判成 json，但不保证真能解析；
        // 解析失败就交给自动识别，别让整块退回无格式文本。
      }
    }
    let html: string
    if (lang) {
      html = hljs.highlight(source, { language: lang }).value
    } else if (source.length > AUTO_MAX_CHARS) {
      lang = 'text'
      html = escapeHtml(source)
    } else {
      const r = hljs.highlightAuto(source, AUTO_LANGS)
      lang = r.language || 'text'
      html = r.value
    }
    const view: ClipboardCodeView = { html, lang }
    if (cache.size >= CACHE_MAX) {
      const oldest = cache.keys().next().value
      if (oldest) cache.delete(oldest)
    }
    cache.set(id, view)
    return view
  } catch {
    // 高亮器内部异常（极少见）不该让整条记录消失：退回纯文本渲染。
    return { html: escapeHtml(text), lang: 'text' }
  }
}
