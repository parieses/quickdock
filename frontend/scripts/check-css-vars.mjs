// CI 门禁：扫描前端源码里「用了但没定义」的 CSS 变量。
//
// 为什么要这道闸：写错一个 token 名（--color-text vs --color-text-primary）
// 浏览器不会报错，只会静默丢掉整条声明——间距没了、文字变成继承色，
// 而且只在特定主题/特定组件下才看得出来。构建期扫一遍最省事。
//
// 规则：
//   - 只统计 var(--x) 且**没有回退值**的用法；var(--x, 16px) 天生允许未定义。
//   - 先剥掉注释（/* */ 与 <!-- -->），避免文档里举例的变量被当成真实用法。
//   - 定义来源：src 下所有 .css 与 .vue/.ts 的 style 块中的 `--x:` 声明。
//
// 用法：node scripts/check-css-vars.mjs（退出码 1 表示有未定义变量）

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, extname } from 'node:path'

const SRC = new URL('../src', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1')

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) walk(p, out)
    else out.push(p)
  }
  return out
}

function stripComments(s) {
  return s
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    // 整行 // 注释（文档里举例的 var(--x) 不该算真实引用）。
    // 只删「行首就是 //」的行：URL 里的 // 不能被误伤。
    .split('\n')
    .filter((line) => !/^\s*\/\//.test(line))
    .join('\n')
}

const files = walk(SRC).filter((f) => ['.vue', '.ts', '.css'].includes(extname(f)))
const defined = new Set()
const used = new Map() // var -> Set<file>

for (const file of files) {
  const raw = stripComments(readFileSync(file, 'utf8'))
  for (const m of raw.matchAll(/(--[A-Za-z0-9_-]+)\s*:/g)) defined.add(m[1])
  for (const m of raw.matchAll(/var\(\s*(--[A-Za-z0-9_-]+)\s*([,)])/g)) {
    if (m[2] === ',') continue // 有回退值，允许未定义
    if (!used.has(m[1])) used.set(m[1], new Set())
    used.get(m[1]).add(file.split(/[\\/]/).slice(-2).join('/'))
  }
}

const missing = [...used.entries()].filter(([name]) => !defined.has(name))
if (missing.length === 0) {
  console.log(`CSS 变量检查通过（${used.size} 个引用 / ${defined.size} 个定义）`)
  process.exit(0)
}
console.error('以下 CSS 变量被引用但从未定义：')
for (const [name, where] of missing) {
  console.error(`  ${name}  ←  ${[...where].join(', ')}`)
}
process.exit(1)
