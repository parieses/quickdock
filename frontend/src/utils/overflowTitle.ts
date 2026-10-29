// 文本被 CSS 截断时，鼠标移入显示完整内容。
//
// 只在「真的溢出」时才挂原生 title：卡片里每条描述都挂 title 的话，
// 没被截断的短描述也会在悬停时冒出灰框，看起来像 bug 而不是帮助。
// line-clamp 截断的元素 scrollHeight 仍按未裁剪内容计算（Chromium/WebView2），
// 所以直接比 clientHeight 就够；万一某个引擎不这么算，退化成「不弹提示」，
// 不会影响布局。
export function applyOverflowTitle(e: MouseEvent, text: string): void {
  const el = e.currentTarget as HTMLElement | null
  if (!el) return
  el.title = el.scrollHeight > el.clientHeight + 1 ? text : ''
}
