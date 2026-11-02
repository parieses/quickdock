// aiSideWidth.ts — AI 右侧面板的宽度计算
//
// 为什么单独成文件：宽度有下限、上限、还要随窗口变化重新收敛，拖动时每帧都要算。
// 放在组件里既不好测也不好复用（落地页/对话页将来可能各有一个面板）。
//
// 三条边界，缺一条都会出问题：
//   1. 下限 340px：与 CSS 的 min-width 同源。再窄标签页标题就只剩几个字，
//      CodeMirror 的横向滚动条会常态出现。
//   2. 上限不能只看比例：窗口 1920 时 70% 是 1186，主区还能剩 508；但窗口
//      缩到 900 时 70% 只给主区留 270，聊天区就废了。所以还要保证主区至少
//      AI_MAIN_MIN_W。
//   3. 容器窄到「下限 + 主区保底」都放不下时（.ai-page 只有 500px 这种），
//      让面板先退，主区优先——面板是可选视图，主区压死就没得救。
//      CSS 的 min-width 会压过 max-width，所以下限本身也得跟着容器收缩，
//      写成 min(340px, 100% - 320px)；这里的 floor 就是同一条公式。

/** 面板最小宽度（px），必须与 .ai-side 的 min-width 上限一致。 */
export const AI_SIDE_W_MIN = 340
/** 面板最多占父容器的比例。 */
export const AI_SIDE_MAX_RATIO = 0.7
/** 主区至少要留下的宽度（px）。 */
export const AI_MAIN_MIN_W = 320
/** 没拖过时的默认宽度占比，必须与 .ai-side 的 width 一致。 */
export const AI_SIDE_DEFAULT_RATIO = 0.42

/**
 * 面板下限：正常是 340，容器放不下时退到「容器宽 - 主区保底」。
 * 与 CSS 的 `min-width: min(340px, calc(100% - 320px))` 是同一个式子——
 * 两边算法一旦分叉，拖动时就会出现「松手跳一下」（CSS 把 JS 给的值再夹一次）。
 */
export function sideWidthFloor(containerW: number): number {
  if (!Number.isFinite(containerW) || containerW <= 0) return AI_SIDE_W_MIN
  // min-width 不接受负值（浏览器会归零），这里也归零，保持两边一致
  return Math.max(0, Math.min(AI_SIDE_W_MIN, containerW - AI_MAIN_MIN_W))
}

/**
 * 把想要的宽度收进合法区间。
 * @param want 期望宽度（px）
 * @param containerW 面板父容器的宽度（px）
 */
export function clampSideWidth(want: number, containerW: number): number {
  const min = sideWidthFloor(containerW)
  // 容器未知（还没挂载 / 被隐藏）时只守默认下限，任何上限推算都没有意义
  if (!Number.isFinite(containerW) || containerW <= 0) return Math.round(Math.max(want, min))
  const max = Math.max(min, Math.min(containerW * AI_SIDE_MAX_RATIO, containerW - AI_MAIN_MIN_W))
  return Math.round(Math.min(Math.max(want, min), max))
}

/** 没拖过时的默认宽度。 */
export function defaultSideWidth(containerW: number): number {
  return clampSideWidth(containerW * AI_SIDE_DEFAULT_RATIO, containerW)
}

/**
 * 从 localStorage 读到的原始值解析成宽度；没有或非法返回 null（表示用默认）。
 * 单独抽出来是因为 localStorage 里什么都可能有（旧版本残留、手改、空串）。
 */
export function parseStoredWidth(raw: string | null | undefined): number | null {
  if (!raw) return null
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? n : null
}
