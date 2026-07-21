import { ref } from 'vue'
import { pinyin } from 'pinyin-pro'
import type { PluginInfo, PluginCommand } from '../types'

export interface PluginCmdIndex {
  plugin: PluginInfo
  cmd: PluginCommand
  regex: RegExp | null
  regexValid: boolean
  pinyinTitleFull: string
  pinyinTitleInit: string
  pinyinKwFull: string[]
  pinyinKwInit: string[]
  pinyinAliasFull: string[]
  pinyinAliasInit: string[]
}

const pluginCmdIndex = ref<PluginCmdIndex[]>([])

function buildPluginIndex(plugins: PluginInfo[]): PluginCmdIndex[] {
  const idx: PluginCmdIndex[] = []
  const seen = new Set<string>()
  for (const plugin of plugins) {
    if (plugin.status !== 'running') continue
    for (const cmd of (plugin.commands || [])) {
      const key = plugin.id + '|' + cmd.id
      if (seen.has(key)) continue
      seen.add(key)
      let regex: RegExp | null = null
      let regexValid = true
      if (cmd.matchPattern) {
        try {
          regex = new RegExp('^(?:' + cmd.matchPattern + ')$')
        } catch (e) {
          console.warn(`[CmdPalette] Invalid matchPattern in plugin "${plugin.id}" cmd "${cmd.id}":`, (e as Error).message)
          regexValid = false
        }
      }
      const titlePy = pinyin(cmd.title, { toneType: 'none', type: 'array' })
      const pinyinTitleFull = titlePy.join('').toLowerCase()
      const pinyinTitleInit = titlePy.map(p => p[0]).join('').toLowerCase()
      const pinyinKwFull: string[] = []
      const pinyinKwInit: string[] = []
      for (const kw of (cmd.keywords || [])) {
        const kwPy = pinyin(kw, { toneType: 'none', type: 'array' })
        pinyinKwFull.push(kwPy.join('').toLowerCase())
        pinyinKwInit.push(kwPy.map(p => p[0]).join('').toLowerCase())
      }
      const pinyinAliasFull: string[] = []
      const pinyinAliasInit: string[] = []
      for (const alias of (cmd.aliases || [])) {
        const aPy = pinyin(alias, { toneType: 'none', type: 'array' })
        pinyinAliasFull.push(aPy.join('').toLowerCase())
        pinyinAliasInit.push(aPy.map(p => p[0]).join('').toLowerCase())
      }
      idx.push({ plugin, cmd, regex, regexValid, pinyinTitleFull, pinyinTitleInit, pinyinKwFull, pinyinKwInit, pinyinAliasFull, pinyinAliasInit })
    }
  }
  return idx
}

function levenshtein(a: string, b: string): number {
  const alen = a.length, blen = b.length
  if (alen === 0) return blen
  if (blen === 0) return alen
  const limit = Math.min(Math.max(alen, blen), 20)
  const s1 = a.slice(0, limit), s2 = b.slice(0, limit)
  const m = s1.length, n = s2.length
  const dp: number[] = Array(n + 1).fill(0).map((_, i) => i)
  for (let i = 1; i <= m; i++) {
    let prev = i
    for (let j = 1; j <= n; j++) {
      const tmp = dp[j]
      dp[j] = s1[i - 1] === s2[j - 1] ? prev : 1 + Math.min(prev, dp[j], dp[j - 1])
      prev = tmp
    }
  }
  return dp[n]
}

function calcPluginScore(
  idx: PluginCmdIndex, q: string, qLC: string
): { score: number; matchType: string; inlineInput?: string } {
  let bestScore = 0
  let bestType = ''
  let inlineInput: string | undefined

  if (!q) return { score: 0, matchType: '' }

  if (idx.cmd.prefix) {
    const prefixLC = idx.cmd.prefix.toLowerCase()
    if (qLC.startsWith(prefixLC) && (qLC.length === prefixLC.length || qLC[prefixLC.length] === ' ')) {
      bestScore = 95; bestType = 'slash'
      inlineInput = q.slice(prefixLC.length).trim() || undefined
    }
  }
  if (idx.regex && idx.regexValid) {
    try {
      if (idx.regex.test(q)) {
        if (idx.cmd.prefix && qLC.startsWith(idx.cmd.prefix.toLowerCase()) && (qLC.length === idx.cmd.prefix.length || qLC[idx.cmd.prefix.length] === ' ')) {
        } else if (bestScore < 85) {
          bestScore = 85; bestType = 'match pattern'; inlineInput = q
        }
      }
    } catch {}
  }
  const titleLC = idx.cmd.title.toLowerCase()
  if (titleLC === qLC && bestScore < 100) { bestScore = 100; bestType = 'exact' }
  if ((idx.cmd.keywords || []).some(k => k.toLowerCase() === qLC) && bestScore < 90) { bestScore = 90; bestType = 'keyword' }
  if ((idx.cmd.aliases || []).some(a => a.toLowerCase() === qLC) && bestScore < 85) { bestScore = 85; bestType = 'alias' }
  if (titleLC.startsWith(qLC) && bestScore < 75) { bestScore = 75; bestType = 'prefix' }
  if ((idx.cmd.keywords || []).some(k => qLC.startsWith(k.toLowerCase() + ' ')) && bestScore < 55) { bestScore = 55; bestType = 'kw inline' }
  if ((idx.cmd.keywords || []).some(k => k.toLowerCase().startsWith(qLC)) && bestScore < 65) { bestScore = 65; bestType = 'kw prefix' }
  if (titleLC.includes(qLC) && bestScore < 60) { bestScore = 60; bestType = 'contains' }
  if ((idx.cmd.keywords || []).slice(0, 20).some(k => k.toLowerCase().includes(qLC)) && bestScore < 40) { bestScore = 40; bestType = 'kw match' }
  if ((idx.cmd.aliases || []).some(a => a.toLowerCase().includes(qLC)) && bestScore < 50) { bestScore = 50; bestType = 'alias' }
  if (idx.plugin.name.toLowerCase().includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'plugin' }
  if ((idx.plugin.description || '').toLowerCase().includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'desc' }
  if (idx.cmd.id.toLowerCase().includes(qLC) && bestScore < 30) { bestScore = 30; bestType = 'id' }
  if ((idx.pinyinTitleFull.includes(qLC) || idx.pinyinTitleInit.startsWith(qLC)) && bestScore < 20) { bestScore = 20; bestType = 'pinyin' }
  if (bestScore < 15) {
    for (let i = 0; i < idx.pinyinKwFull.length; i++) {
      if (idx.pinyinKwFull[i].includes(qLC) || idx.pinyinKwInit[i].startsWith(qLC)) {
        bestScore = Math.max(bestScore, 15); bestType = 'kw pinyin'; break
      }
    }
  }
  if (bestScore < 12) {
    if (idx.pinyinAliasFull.some(p => p.includes(qLC)) || idx.pinyinAliasInit.some(p => p.startsWith(qLC))) {
      bestScore = Math.max(bestScore, 12); bestType = 'alias py'
    }
  }
  if (bestScore === 0 && qLC.length >= 3) {
    const hasChinese = /[\u4e00-\u9fff]/.test(qLC)
    const threshold = hasChinese ? 3 : 2
    const fuzzyLimit = 20
    if (levenshtein(titleLC.slice(0, fuzzyLimit), qLC.slice(0, fuzzyLimit)) <= threshold) { bestScore = 10; bestType = 'fuzzy' }
    else if ((idx.cmd.keywords || []).some(k => levenshtein(k.toLowerCase().slice(0, fuzzyLimit), qLC.slice(0, fuzzyLimit)) <= threshold)) { bestScore = 8; bestType = 'fuzzy' }
  }
  return { score: bestScore, matchType: bestType, inlineInput }
}

const matchTypeLabels: Record<string, string> = {
  'exact': '精确', 'keyword': '关键字', 'alias': '别名', 'prefix': '前缀',
  'contains': '包含', 'fuzzy': '模糊', 'match pattern': '正则', 'kw inline': '内联',
  'kw prefix': '关键字前缀', 'kw match': '关键字', 'kw pinyin': '拼音',
  'alias py': '拼音', 'pinyin': '拼音', 'plugin': '插件', 'desc': '描述',
  'id': 'ID', 'slash': '命令',
}

export function usePluginIndex() {
  return { pluginCmdIndex, buildPluginIndex, calcPluginScore, levenshtein, matchTypeLabels }
}
