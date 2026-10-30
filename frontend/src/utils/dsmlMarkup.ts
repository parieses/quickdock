// 剥离「被模型写成正文的工具调用」标记。
//
// 只有极少数情况会出现：模型想调工具，但请求里没有 tools 字段（比如工具轮次已经
// 用尽被撤下），它就把调用输出成一段纯文本标记：
//
//   骨架文件已经建好，我开始逐段填入内容。
//   <｜｜DSML｜｜ calls>
//   <｜｜DSML｜｜ invoke name="file_read">
//   <｜｜DSML｜｜ parameter name="path" string="true">quickdock-guide.md</｜｜DSML｜｜ parameter>
//   </｜｜DSML｜｜ invoke>
//   </｜｜DSML｜｜ calls>
//
// 后端已经在流式解析阶段拦下来并还原成真正的工具调用（services/ai/ai_dsml.go），
// 这里只兜底**已经落库的历史消息**：那些消息是在修复之前存下的，直接渲染就是
// 满屏 `<｜｜DSML｜｜>`，用户回看会以为功能坏了。
//
// 只剥标记块、不动其它任何字：宁可留着多余空行，也不要误删用户的内容。

// TAG 一个 DSML 标记标签的前缀片段。
// 竖线数量与全角/半角都不固定（实测全角双竖线，规范形式是单竖线）。
const TAG = '[|｜]{0,4}\\s*DSML\\s*[|｜]{0,4}'

/** 成对的 calls 块（内部含所有 invoke / parameter）。 */
const CALLS_BLOCK = new RegExp(`<${TAG}\\s*(?:tool_)?calls\\s*>[\\s\\S]*?</${TAG}\\s*(?:tool_)?calls\\s*>`, 'gi')

/** 单条 invoke 块（模型有时不写外层 calls）。 */
const INVOKE_BLOCK = new RegExp(`<${TAG}\\s*invoke\\b[\\s\\S]*?</${TAG}\\s*invoke\\s*>`, 'gi')

/** 没闭合的 calls 块：流被打断时留下的残片，一路剥到结尾。 */
const CALLS_OPEN_TAIL = new RegExp(`<${TAG}\\s*(?:tool_)?calls\\s*>[\\s\\S]*$`, 'i')

export function stripDsmlMarkup(text: string): string {
  if (!text || !/DSML/i.test(text)) return text
  return text.replace(CALLS_BLOCK, '').replace(INVOKE_BLOCK, '').replace(CALLS_OPEN_TAIL, '')
}
