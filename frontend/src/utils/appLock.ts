/**
 * 应用锁定态的前端镜像（非响应式，只给事件处理函数做短路判断）。
 *
 * 为什么需要它：锁层只是视觉覆盖层，挡不住挂在 `document` / `window` 上的
 * **捕获阶段**键盘监听。那些监听的判断依据都是「事件目标是不是我」，而锁定
 * 时用户在密码框里打字，目标是密码框——于是命令面板把回车当成「执行当前选中项」、
 * 剪贴板面板把回车当成「复制选中项并关闭窗口」、终端把按键发给远端会话。
 * 实测表现就是「输完密码一按回车，窗口直接关了」。
 *
 * 捕获阶段在 document 上的监听早于锁层根元素，`stopPropagation` 在锁层内
 * 只能拦冒泡，拦不住它们（同元素同阶段的监听要 `stopImmediatePropagation`
 * 才能打断，而那会连输入框一起废掉）。所以这些监听必须自己先问一句锁没锁。
 *
 * 真相源仍是 Go 侧：这里只由 LockScreen 的挂载/卸载同步，和「锁层可见」严格一致。
 */
let locked = false

/** 由 LockScreen 在挂载/卸载时调用，保证与锁层可见性同步。 */
export function setAppLocked(value: boolean): void {
  locked = value
}

/** 锁定态下，所有 document / window 级键盘监听应首行 return。 */
export function isAppLocked(): boolean {
  return locked
}
