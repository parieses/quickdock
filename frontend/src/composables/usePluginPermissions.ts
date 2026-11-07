/**
 * 安装前权限二次确认的共享状态。
 *
 * 确认框实例只有一个，放在常驻的插件管理页里；市场页是它的子组件、同样常驻于
 * 同一视图栈，因此两处安装入口（本地 zip / 市场安装·更新）可以共用这一个对话框。
 * 用 Promise 而不是事件冒泡：调用方 `if (!(await confirmPermissions(lines))) return`
 * 就能把「不确认就取消」写成一条直线，不会漏掉某个分支。
 *
 * 安全边界：这里只负责把声明「摆出来」，不替用户判断、不默认同意；运行期的
 * 三道闸（声明 permissions.mcp → 等级门 → 插件路径硬拒 LevelRisk）由后端独立把守。
 */
import { ref } from 'vue'

export const permDialog = ref<{
  visible: boolean
  details: string[]
  titleKey: string
}>({ visible: false, details: [], titleKey: 'pluginPermConfirmTitle' })

let resolver: ((ok: boolean) => void) | null = null

/** 弹出确认框，返回用户是否点了「确认」。取消 / 关闭 / 被新请求顶掉都算 false。 */
export function confirmPermissions(details: string[], titleKey = 'pluginPermConfirmTitle'): Promise<boolean> {
  // 已有对话框还在等待时先收尾：否则旧 Promise 永远不 resolve，安装流程会卡死。
  if (resolver) resolver(false)
  permDialog.value = { visible: true, details, titleKey }
  return new Promise((resolve) => { resolver = resolve })
}

export function resolvePermissionConfirm(ok: boolean) {
  permDialog.value.visible = false
  resolver?.(ok)
  resolver = null
}

/**
 * 把 permissions 声明逐项翻成「key: JSON 值」。
 *
 * 刻意展示原始 JSON 而不自造文案：权限有 bool / 白名单 / scope 三种形态，
 * 自造文案容易在新增形态（2026-11-07 的 permissions.mcp）时漏翻——漏一项
 * 就等于替用户默认同意了那一项。selfAudit / skipReason 也照列：
 * selfAudit=skip 会关掉 native 插件的安装期静态自证，是最该让用户看见的一条。
 */
export function permissionsToLines(perms?: Record<string, unknown>): string[] {
  if (!perms) return []
  const out: string[] = []
  for (const [k, v] of Object.entries(perms)) {
    if (v === null || v === undefined) continue
    let val: string
    try {
      val = JSON.stringify(v)
    } catch {
      val = String(v)
    }
    out.push(`${k}: ${val}`)
  }
  return out
}

export function usePluginPermissions() {
  return { permDialog, confirmPermissions, resolvePermissionConfirm, permissionsToLines }
}
