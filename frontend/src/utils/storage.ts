/**
 * localStorage 的安全读写封装。
 *
 * 背景：项目里大量 pref 写都是 setItem 外包一层 try/catch 静默吞异常，
 * 且必须吞掉异常——隐私模式 / 配额满时写不进去只应退化为「本次运行有效」，不能打断交互。
 * 这里把这段逐字重复的兜底收口。
 *
 * 刻意**不**封装读：读侧每处都有自己的默认值与校验口径（只认 '0'、数组过滤、宽度区间
 * 校验……），强行统一会悄悄改变行为。读仍留在调用点，只有「写入吞异常」在这里收敛。
 *
 * 与 storageQueue.ts 的分工：那是「同一 key 高频写、150ms 合并落盘」的热路径；这里是
 * 一次性 pref 写。高频场景请用 writeStorageDebounced，不要改用这里。
 */

/** 写字符串 pref；隐私模式 / 配额满时静默失败，仅本次运行有效。 */
export function writeStored(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* 写不进去不影响功能，保持与逐点 try/catch 完全一致的语义 */
  }
}

/** 删 pref；同样吞掉存储被禁的场景。 */
export function removeStored(key: string): void {
  try {
    localStorage.removeItem(key)
  } catch {
    /* ignore */
  }
}
