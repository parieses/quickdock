import { ref } from 'vue'

/**
 * SSH 终端字号：一个进程内共享的偏好。
 *
 * 为什么不在 SshTerminal 里各存各的（原来就是那样）：字号挂在 localStorage 上，
 * 但每个 SshTerminal 实例都在 setup 时读一次快照 → 调整只对「当前这个终端」
 * 以及之后新建的终端生效，已经开着的另外几个标签页不会跟着变。多标签并排看日志时
 * 这种不一致很刺眼。
 *
 * 这里用模块级 ref 做唯一来源：
 *   - SshTerminal 监听它 → 所有已挂载终端同步改字号（xterm 改 options 后还要 fit，
 *     并同步 resized 给远端，所以必须走终端自己的 applyFontSize，不能只改 CSS）；
 *   - 标签栏的 A- / A+ 直接写它 → 一处调整，全窗口生效。
 *
 * 仍然存 localStorage 而不是落库：字号属于「这台机器这块屏」的显示偏好，
 * 跟着数据目录迁移或同步到别的机器都没有意义。
 */
const FONT_SIZE_KEY = 'qd_ssh_fontsize'
export const SSH_FONT_MIN = 10
export const SSH_FONT_MAX = 24
export const SSH_FONT_DEFAULT = 13

function clamp(n: number): number {
  return Math.min(SSH_FONT_MAX, Math.max(SSH_FONT_MIN, Math.round(n)))
}

function readStored(): number {
  try {
    const v = Number(localStorage.getItem(FONT_SIZE_KEY))
    if (Number.isFinite(v) && v >= SSH_FONT_MIN && v <= SSH_FONT_MAX) return v
  } catch {
    /* 隐私模式 / localStorage 被禁用：用默认值，不影响使用 */
  }
  return SSH_FONT_DEFAULT
}

export const sshFontSize = ref(readStored())

/** 改字号：夹到合法区间后写共享状态 + 落盘。重复设同一个值是幂等的。 */
export function setSshFontSize(next: number): void {
  const size = clamp(next)
  if (size === sshFontSize.value) return
  sshFontSize.value = size
  try {
    localStorage.setItem(FONT_SIZE_KEY, String(size))
  } catch {
    /* 存不住也能用，只是下次启动回默认 */
  }
}

export function useSshFontSize() {
  return { sshFontSize, setSshFontSize, SSH_FONT_MIN, SSH_FONT_MAX }
}
