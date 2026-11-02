// 多条任务同时跑时，决定「眼前这一页该画哪一条」。
//
// 抽成纯函数是因为这条规则有两个容易写错的分支（见下），而它们一旦写错，
// 表现是「回答串到别的任务上」——最贵的一种 bug，且只靠点界面很难复现。
//
// 规则：
//   · 有会话 id → 只认这个会话自己的那条流。别的任务在跑与这一页无关。
//   · 没有会话 id（停在落地页）→ 只认**这一张空白草稿**发起的那条：
//       新任务在 conv 事件回来之前没有 id，若按「随便哪条没有 id 的流」去找，
//       连开两条新任务时两条会互相顶掉，甚至把刚发的字画到另一条上。
export interface RunLike {
  /** 本地唯一 id（新任务在拿到会话 id 之前只有它） */
  rid: string
  /** 会话 id（新任务拿到之前是空串） */
  convId: string
}

export function pickActiveRun<T extends RunLike>(
  runs: readonly T[],
  activeId: string,
  draftRunId: string,
): T | undefined {
  if (activeId) return runs.find((r) => r.convId === activeId)
  return runs.find((r) => r.rid === draftRunId)
}
