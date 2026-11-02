import type { AIArtifact } from '../types/ai'

// 产物签名：把一条会话的产物列表压成「路径#调用id」多行文本。
//
// 用途只有一个——判断「已经打开的文件该不该重读」，所以要的不是路径集合，
// 而是每一条的**版本**：
//   · 只看路径集合不够：同一个文件被改第二次，集合一模一样；
//   · 也不能直接拿产物数组当 watch 源：流式期间每来一条实时输出，
//     父组件的 computed 就会重建一次数组，拿它当信号会把文件读成筛子。
//
// 写（file_write）和改（file_edit）都算一次新版本：对读者来说磁盘内容变了，
// 谁动的手不重要。
export function artifactSigOf(arts: AIArtifact[]): string {
  return arts
    // 还没跑完的调用不算：file_write 正在写入时磁盘上可能只有半截内容，
    // 等它结束（done 变 true）签名再变一次，那时才值得重读。
    .filter((a) => a.done !== false)
    .map((a) => a.path + '#' + (a.callId || ''))
    .join('\n')
}

// 把签名拆回 路径 → 调用id。用 lastIndexOf 切分：Windows 路径里可能出现 #，
// 调用 id 是后端生成的，不含这个字符。
function parseSig(sig: string): Map<string, string> {
  const m = new Map<string, string>()
  if (!sig) return m
  for (const line of sig.split('\n')) {
    const i = line.lastIndexOf('#')
    if (i > 0) m.set(line.slice(0, i), line.slice(i + 1))
  }
  return m
}

// changedArtifactPaths 返回「签名变过」的路径，也就是这些文件在这一轮里被重写过。
//
// 只返回新签名里存在的路径：产物列表只增不减（后端按路径去重、后写覆盖先写），
// 万一某条消失，那是历史重算造成的，不该据此去读一个可能已经不存在的文件。
export function changedArtifactPaths(prevSig: string, nextSig: string): string[] {
  const before = parseSig(prevSig)
  const after = parseSig(nextSig)
  const out: string[] = []
  for (const [path, callId] of after) {
    if (before.get(path) !== callId) out.push(path)
  }
  return out
}
