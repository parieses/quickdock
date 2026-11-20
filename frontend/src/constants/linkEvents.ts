/**
 * 实体互链候选缓存的失效信号。
 *
 * EntityLinkPicker 首次打开候选时拉一次三类实体并缓存 60s；期间新建的笔记 /
 * 待办 / 条目进不了下拉。新建成功的宿主派发这个事件，选择器收到后即时清缓存，
 * 下次打开重拉——避免「刚建完就搜不到」。
 *
 * 派发方（新建成功后）：workspace.addItem / TodoPage.createTodo /
 * NoteManagerPage.newDocAt 与 createFromTemplate。
 */
export const LINK_TARGETS_INVALIDATED = 'quickdock:link-targets-invalidated'

/** 通知互链候选缓存失效。无监听方时是安全的空操作。 */
export function invalidateLinkTargets(): void {
  window.dispatchEvent(new Event(LINK_TARGETS_INVALIDATED))
}
