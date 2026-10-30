// CSV 导出（Excel 友好）。
//
// 三个必须照顾的点，缺一个用户就会觉得"导出的文件打不开"：
// 1. BOM：不带 BOM 的 utf-8 CSV 在中文 Windows 的 Excel 里是乱码。
// 2. CRLF：Excel 对纯 LF 的解析在各版本上不一致，按 CRLF 写最稳。
// 3. 转义：值里含逗号/引号/换行时必须整格加引号，内部引号翻倍，
//    否则一条带逗号的错误输出会把整行挤成两列。

import { downloadBlob } from './download'

/** 把二维数组导成 CSV 并触发下载。第一行通常是表头。 */
export function downloadCsv(filename: string, rows: (string | number)[][]): void {
  const cell = (v: string | number): string => {
    const s = v === null || v === undefined ? '' : String(v)
    return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
  }
  const text = rows.map((r) => r.map(cell).join(',')).join('\r\n')
  // 落盘统一走 utils/download：a 挂 DOM 与延后 revoke 这两个坑只在那一处处理。
  downloadBlob(filename, new Blob(['\uFEFF' + text], { type: 'text/csv;charset=utf-8;' }))
}
