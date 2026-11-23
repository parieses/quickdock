// 文本被 CSS 截断时，鼠标移入显示完整内容。
//
// 只在「真的溢出」时才挂原生 title：卡片里每条描述都挂 title 的话，
// 没被截断的短描述也会在悬停时冒出灰框，看起来像 bug 而不是帮助。
//
// 两个方向都要判：
// - 纵向：line-clamp 截断的元素 scrollHeight 仍按未裁剪内容计算（Chromium/WebView2），
//   直接比 clientHeight 就够；
// - 横向：单行 ellipsis（white-space: nowrap + text-overflow）只体现在 scrollWidth，
//   高度永远相等，只比 clientHeight 会把「一行被截掉的标题」判成没溢出。
// 两个方向都不溢出才清空 title。万一某个引擎算不准某一边，只会退化成多弹/少弹，
// 不影响布局。
export function applyOverflowTitle(e: MouseEvent, text: string): void {
  const el = e.currentTarget as HTMLElement | null
  if (!el) return
  const clippedY = el.scrollHeight > el.clientHeight + 1
  const clippedX = el.scrollWidth > el.clientWidth + 1
  // 没溢出时必须删掉属性而不是置空串：title="" 也算「本元素自带了提示」，
  // 会把父元素（如卡片的「双击打开」）的 title 拦下来不冒泡。
  if (clippedY || clippedX) el.title = text
  else el.removeAttribute('title')
}
