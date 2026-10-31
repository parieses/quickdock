// 回答正文里 markdown 表格的增强：横向滚动容器 / 表头点击排序 / 导出 CSV。
//
// 这些作用在 v-html 渲染出的真实 DOM 上（Vue 不托管），故走原生 API。
// 抽成独立模块是为了能单独测（排序是纯 DOM 操作，不依赖页面状态）；
// 文案与提示由调用方以 deps 注入，本文件不引 i18n / toast。
import { downloadBlob } from './elementImage'

export interface TableEnhanceDeps {
  // 导出按钮的文案（i18n 由调用方给：本文件不引 useI18n）
  exportLabel: string
  // 导出成功后的提示（toast 由调用方给），name 是导出的文件名
  onExported: (name: string) => void
}

function csvCell(s: string): string {
  const v = (s || '').replace(/\r?\n/g, ' ').trim()
  return /[",\r\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v
}

export function exportTableCsv(tbl: HTMLTableElement, onExported: (name: string) => void) {
  const lines = Array.from(tbl.querySelectorAll('tr')).map((r) =>
    Array.from(r.children)
      .map((c) => csvCell(c.textContent ?? ''))
      .join(','),
  )
  const csv = '﻿' + lines.join('\r\n') // 前导 BOM：避免 Excel 把中文 CSV 读成乱码
  const name = 'table.csv'
  downloadBlob(new Blob([csv], { type: 'text/csv;charset=utf-8' }), name)
  onExported(name)
}

export function sortTableByColumn(th: HTMLElement) {
  const tbl = th.closest('table') as HTMLTableElement | null
  if (!tbl) return
  const col = Number(th.dataset.col ?? '0')
  const tbody = (tbl.querySelector('tbody') as HTMLTableSectionElement | null) || tbl
  const rows = Array.from(tbody.querySelectorAll('tr'))
  if (!rows.length) return
  const prevCol = tbl.dataset.sortCol
  const prevDir = tbl.dataset.sortDir
  const dir = prevCol === String(col) && prevDir === 'asc' ? 'desc' : 'asc'
  const numeric = rows.every((r) => {
    const v = (r.children[col]?.textContent ?? '').trim()
    return v !== '' && !Number.isNaN(Number(v))
  })
  rows.sort((a, b) => {
    const av = (a.children[col]?.textContent ?? '').trim()
    const bv = (b.children[col]?.textContent ?? '').trim()
    const cmp = numeric ? Number(av) - Number(bv) : av.localeCompare(bv, undefined, { numeric: true })
    return dir === 'asc' ? cmp : -cmp
  })
  rows.forEach((r) => tbody.appendChild(r))
  tbl.dataset.sortCol = String(col)
  tbl.dataset.sortDir = dir
  // 指示符：只有当前列显示 ▲/▼，其余清掉
  tbl.querySelectorAll('.md-th-sortable').forEach((el) => {
    if (el !== th) el.removeAttribute('data-sort-dir')
  })
  th.setAttribute('data-sort-dir', dir)
}

// enhanceTables 扫描 root 下还没增强过的表格。
// 已处理的表打 data-tbl-enhanced，避免流式每帧重扫时重复注入。
export function enhanceTables(root: HTMLElement | null, deps: TableEnhanceDeps) {
  if (!root) return
  const tables = root.querySelectorAll('table:not([data-tbl-enhanced])')
  for (const tbl of Array.from(tables) as HTMLTableElement[]) {
    tbl.setAttribute('data-tbl-enhanced', '1')
    const parent = tbl.parentElement
    // 横向滚动容器：表格可能比气泡宽，不包一层表格自己不会滚
    let wrap = parent?.classList.contains('md-table-wrap')
      ? (parent as HTMLElement)
      : null
    if (!wrap && parent) {
      wrap = document.createElement('div')
      wrap.className = 'md-table-wrap'
      parent.insertBefore(wrap, tbl)
      wrap.appendChild(tbl)
    }
    // 表头排序：给每个 thead th 打列索引 + 可点标记（marked 表格第一行即 thead>th）
    tbl.querySelectorAll('thead th').forEach((th, i) => {
      th.setAttribute('data-col', String(i))
      th.classList.add('md-th-sortable')
    })
    // 导出 CSV 按钮条（插在滚动容器上方）
    if (wrap && !wrap.previousElementSibling?.classList.contains('md-table-bar')) {
      const bar = document.createElement('div')
      bar.className = 'md-table-bar'
      const btn = document.createElement('button')
      btn.type = 'button'
      btn.className = 'md-table-export'
      btn.textContent = deps.exportLabel
      btn.addEventListener('click', () => { void exportTableCsv(tbl, deps.onExported) })
      bar.appendChild(btn)
      wrap.parentElement?.insertBefore(bar, wrap)
    }
  }
}
