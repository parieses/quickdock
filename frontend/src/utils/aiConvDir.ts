// 「这条任务的文件在哪个目录」——侧栏右键「在资源管理器打开」用的解析规则。
//
// 抽成纯函数是因为这条规则要跟后端对齐，而两边写岔了不会报错，只会打开一个空目录
// 或者干脆打不开（用户只会觉得「点了没反应」）。
//
// 规则与后端一致（services/ai/agent_run.go）：
//   · 会话自己的 cwd 优先。开了隔离工作区后后端会把 cwd 改成工作区路径，
//     拆掉工作区又会改回主仓库 —— 所以 cwd 恒等于「此刻真正在干活的目录」。
//   · cwd 为空才回退到所属空间的目录（老数据：只有 space_id，没有 cwd）。
//   · 两者都没有 → 空串。前端据此把菜单项置灰，而不是拿空路径去调系统接口。
//
// 刻意不读 worktree_path：那只是历史记录，工作区被清理后它可能还留着，
// 拿它打开只会落到一个不存在的路径上。
export interface ConvDirSource {
  cwd?: string
  space_id?: string
}

export interface ConvDirSpace {
  id: string
  cwd: string
}

export function resolveConvDir(c: ConvDirSource | null | undefined, spaces: readonly ConvDirSpace[]): string {
  if (!c) return ''
  const own = (c.cwd || '').trim()
  if (own) return own
  const sid = (c.space_id || '').trim()
  if (!sid) return ''
  return (spaces.find((s) => s.id === sid)?.cwd || '').trim()
}
