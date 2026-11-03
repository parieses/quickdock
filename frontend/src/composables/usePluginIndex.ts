import { logErr } from '../utils/logger'
import { ref } from 'vue'
import { pinyinParts } from '../utils/pinyin'
import type { PluginInfo, PluginCommand } from '../types'
import { logWarn } from '../utils/logger'

export interface PluginCmdIndex {
  plugin: PluginInfo
  cmd: PluginCommand
  regex: RegExp | null
  regexValid: boolean
  /** 插件当前未运行（status !== 'running'）：命令仍保留在索引中，带此标记供列表标注/执行前自动拉起 */
  notRunning: boolean
  titleLc: string[]           // 原始标题 + titleI18n 各语言值（小写），供多语言搜索
  pinyinTitleFull: string
  pinyinTitleInit: string
  pinyinKwFull: string[]
  pinyinKwInit: string[]
  pinyinAliasFull: string[]
  pinyinAliasInit: string[]
  // 预计算小写化字段（P-1，2026-11-03）：calcPluginScore 对每个 query 都会对每条命令
  // 逐字段 toLowerCase()，200+ 命令 × 多个字段 = 每次击键上千次无效小写化。这些与
  // query 无关，只在建索引时算一次（与 titleLc / lcIndex 同思路）。
  keywordsLC: string[]
  aliasesLC: string[]
  pluginNameLC: string
  pluginDescLC: string
  cmdIdLC: string
}

const pluginCmdIndex = ref<PluginCmdIndex[]>([])

function buildPluginIndex(plugins: PluginInfo[]): PluginCmdIndex[] {
  const idx: PluginCmdIndex[] = []
  const seen = new Set<string>()
  for (const plugin of plugins) {
    // 非 running 插件不静默丢弃：命令保留（notRunning 标记），列表标注「未运行」，
    // 选中执行前由面板自动 EnablePlugin 拉起，避免「命令从面板里消失」的困惑
    const notRunning = plugin.status !== 'running'
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
          logWarn('matchPattern', `plugin="${plugin.id}" cmd="${cmd.id}" error:`, (e as Error).message)
          regexValid = false
        }
      }
      const titlePy = pinyinParts(cmd.title)
      const pinyinTitleFull = titlePy.full
      const pinyinTitleInit = titlePy.init
      // 多语言标题搜索键：原始标题 + title_i18n 所有语言值
      const titleLc = [cmd.title, ...Object.values(cmd.titleI18n || {})].map(s => s.toLowerCase())
      const pinyinKwFull: string[] = []
      const pinyinKwInit: string[] = []
      for (const kw of (cmd.keywords || [])) {
        const kwPy = pinyinParts(kw)
        pinyinKwFull.push(kwPy.full)
        pinyinKwInit.push(kwPy.init)
      }
      const pinyinAliasFull: string[] = []
      const pinyinAliasInit: string[] = []
      for (const alias of (cmd.aliases || [])) {
        const aPy = pinyinParts(alias)
        pinyinAliasFull.push(aPy.full)
        pinyinAliasInit.push(aPy.init)
      }
      const keywordsLC = (cmd.keywords || []).map(k => k.toLowerCase())
      const aliasesLC = (cmd.aliases || []).map(a => a.toLowerCase())
      const pluginNameLC = plugin.name.toLowerCase()
      const pluginDescLC = (plugin.description || '').toLowerCase()
      const cmdIdLC = cmd.id.toLowerCase()
      idx.push({ plugin, cmd, regex, regexValid, notRunning, titleLc, pinyinTitleFull, pinyinTitleInit, pinyinKwFull, pinyinKwInit, pinyinAliasFull, pinyinAliasInit, keywordsLC, aliasesLC, pluginNameLC, pluginDescLC, cmdIdLC })
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

// ---- 结构化内容嗅探 ----
//
// 插件匹配主要建立在「用户在敲命令名 / 关键字」这个前提上：等值、前缀、子串比对的
// 都是短查询。把一整段 JSON / SQL / JWT 粘进 Ctrl+K 时，它跟任何一条 keywords 都
// 对不上，结果就是「明明装着 JSON 工具箱，敲 json 能出，粘 JSON 却一条都不出」。
//
// 这里补一条内容识别通道：query 本身是一段结构化文本时，用探测器表识别它的**格式名**
// （json / sql / url …），再去命中命令声明的格式线索（keywords / aliases / 标题）。
// 分数固定 72 —— 越过「最佳匹配」的 70 门槛（命中才看得见），但仍低于显式关键字
// 等值（90）与 matchPattern 内容命中（93）：这是「猜你想用它处理这段内容」，
// 不该压过用户真在敲命令名 / 正则精准命中的场景。
//
// 缓存按 query 字符串做：calcPluginScore 对每条命令各调一次，不缓存的话同一段
// 几十 KB 的 JSON 会被逐条探测器跑几十遍。
const CONTENT_SNIFF_MAX_CHARS = 200_000
let lastSniffQuery = ''
let lastSniffFormat: string | null = null

/**
 * matchPattern 命中的「内容形态」门槛：达到即视为粘了内容（93 分档），否则按短词（88 分档）。
 *
 * 取 6 而不是 8：`a@b.com` 只有 7 个字符，但它百分之百是「粘进来的一个邮箱」，
 * 不是用户在敲命令名。真正的短词命令（json / sql / base64）都在 4 字符上下，
 * 6 这个切点既能让邮箱进 93 档，又不会把 `json` 抢到正则命中上去。
 */
const MATCH_PATTERN_CONTENT_MIN_CHARS = 6

/**
 * 正则匹配的输入上限：超过就整条跳过正则。
 *
 * 插件侧的正则都带 `[\s\S]*` 包络，其中不少含 `\s*…\s*` 这类可回溯结构；
 * 往 Ctrl+K 里粘一个 200KB 的 JSON 时，一条正则就可能把主线程卡住几百毫秒
 * （面板每次输入都要对每条命令各跑一遍）。这个量级的输入本来也不需要靠正则分类：
 * 内容嗅探（FORMAT_PROBES）跑的是锚定首尾的轻量探测，不受影响。
 */
const MATCH_PATTERN_MAX_INPUT_CHARS = 8_192

/** 识别「整段内容」的格式名（json / jwt / url / email / ip / xml / sql / base64 / cron / timestamp / hash / color），不是内容返回 null。 */
export function detectContentFormat(q: string): string | null {
  if (q === lastSniffQuery) return lastSniffFormat
  lastSniffQuery = q
  lastSniffFormat = sniffContentFormat(q)
  return lastSniffFormat
}

interface FormatProbe {
  name: string
  /** 首字符白名单：s[0] 不在其中直接跳过（null = 任意首字符）。按首字符分派，避免每个 query 跑满全部探测器。 */
  first: string | null
  test: (s: string) => boolean
}

// 探测器全部锚定首尾并要求形态特征，避免把普通句子误判成 sql / cron。
// 顺序即优先级，按「形态越具体越靠前」排：hash（固长 hex）必须排在 base64 之前
// （32 位 hex 必然满足 base64 的 [A-Za-z0-9+/]{16,}），否则 md5/sha 永远探不到。
const FORMAT_PROBES: FormatProbe[] = [
  { name: 'json', first: '{[', test: s => { try { const p = JSON.parse(s); return p !== null && typeof p === 'object' } catch { return false } } },
  { name: 'jwt', first: null, test: s => /^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*$/.test(s) },
  { name: 'url', first: 'hH', test: s => /^https?:\/\/\S+$/i.test(s) },
  { name: 'email', first: null, test: s => /^[^@\s]+@[^@\s.]+\.[^@\s]+$/.test(s) },
  { name: 'ip', first: '0123456789', test: s => /^(\d{1,3}\.){3}\d{1,3}(:\d{1,5})?$/.test(s) },
  { name: 'xml', first: '<', test: s => (/^<\?xml|^<[a-zA-Z][\w:-]*[\s>]/.test(s)) && /<\/[a-zA-Z][\w:-]*>\s*$/.test(s) },
  { name: 'sql', first: 'sSiIuUdDcCaAwW', test: s => /^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|WITH)\b/i.test(s) },
  { name: 'hash', first: '0123456789aAbBcCdDeEfF', test: s => /^[a-f0-9]{32}$|^[a-f0-9]{40}$|^[a-f0-9]{64}$/i.test(s) },
  { name: 'base64', first: null, test: s => /^(data:[\w/+.-]+;base64,)?[A-Za-z0-9+/]{16,}={0,2}$/.test(s) },
  { name: 'cron', first: null, test: s => (/^\s*\S+(\s+\S+){4,5}\s*$/.test(s)) && /[\d*]/.test(s) },
  { name: 'timestamp', first: '0123456789', test: s => /^\d{10}$|^\d{13}$/.test(s) },
  { name: 'color', first: '#rRhH', test: s => /^#[0-9a-f]{3,8}$/i.test(s) || /^rgba?\(/i.test(s) || /^hsla?\(/i.test(s) },
]

function sniffContentFormat(q: string): string | null {
  const s = q.trim()
  // 首尾必须成对：省掉对普通句子的 parse 开销，也避免 `[1,2` 这类残片误判
  if (s.length < 2 || s.length > CONTENT_SNIFF_MAX_CHARS) return null
  const head = s[0]
  for (const probe of FORMAT_PROBES) {
    if (probe.first !== null && !probe.first.includes(head)) continue
    if (probe.test(s)) return probe.name
  }
  return null
}

export interface PluginScore {
  score: number
  matchType: string
  /** 作为**后端命令参数**带入的文本：只有声明了 acceptsInput 的命令才有 */
  inlineInput?: string
  /**
   * 命中文本本身（正则命中 / 内容嗅探命中时的 query 原文），与 acceptsInput 无关。
   *
   * 与 inlineInput 分开的理由：前端插件靠 `plugin:init.text` 自己读待处理内容
   * （json-toolbox 这类「仅界面」命令根本没声明 acceptsInput，但它能接文本）；
   * 后端执行路径则严格要求 acceptsInput —— 凭内容嗅探就给无前端插件多塞一个参数，
   * 会让命令语义突变。
   */
  hitText?: string
}

function calcPluginScore(
  idx: PluginCmdIndex, q: string, qLC: string
): PluginScore {
  let bestScore = 0
  let bestType = ''
  let inlineInput: string | undefined
  let hitText: string | undefined

  if (!q) return { score: 0, matchType: '' }

  if (idx.cmd.prefix) {
    const prefixLC = idx.cmd.prefix.toLowerCase()
    if (qLC.startsWith(prefixLC) && (qLC.length === prefixLC.length || qLC[prefixLC.length] === ' ')) {
      // 同前缀多命令分流（2026-08-25 P3）：
      //   - 带参查询（'/port 8080'）：只有 acceptsInput 命令才配得上参数 → 加权 98；
      //     无参命令（如打开工具面板类）直接不匹配，避免 '/port 8080' 时 port-list 也并列 95 分
      //   - 纯前缀查询（'/port'）：无参命令保持 95 直达；acceptsInput 命令降权 88（参数未就绪，稍等输入）
      const hasArg = qLC.length > prefixLC.length
      if (idx.cmd.acceptsInput) {
        if (hasArg) {
          bestScore = 98; bestType = 'slash'
          inlineInput = q.slice(prefixLC.length).trim() || undefined
        } else {
          bestScore = 88; bestType = 'slash'
        }
      } else if (!hasArg) {
        bestScore = 95; bestType = 'slash'
      }
    }
  }
  // matchPattern 分档（2026-11-03）：
  //   - prefix 命中（如 `/port 8080`）时正则不再参与打分：slash 档语义已完整，
  //     原实现是「空 if 分支」，这里显式化为 prefixHit 变量；
  //   - 内容形态（长输入 = 用户粘了一段东西）给 93：高于 keyword 等值 90、低于 slash 带参 98；
  //   - 短词命中给 88：低于 keyword 90，避免敲 "json" 被某条宽松正则抢走。
  const prefixHit = !!idx.cmd.prefix && qLC.startsWith(idx.cmd.prefix.toLowerCase())
    && (qLC.length === idx.cmd.prefix.length || qLC[idx.cmd.prefix.length] === ' ')
  if (idx.regex && idx.regexValid && !prefixHit && q.length <= MATCH_PATTERN_MAX_INPUT_CHARS) {
    try {
      if (idx.regex.test(q)) {
        const isContent = q.length >= MATCH_PATTERN_CONTENT_MIN_CHARS
        const sc = isContent ? 93 : 88
        if (bestScore < sc) {
          bestScore = sc; bestType = isContent ? 'match pattern' : 'pattern short'
          // 命中文本一律记下（前端插件可自行取用）；作为后端参数带入仍只认 acceptsInput
          hitText = q
          if (idx.cmd.acceptsInput) inlineInput = q
        }
      }
    } catch (e) { logErr('usePluginIndex', e) }
  }
  // 标题命中辅助：原始标题与 title_i18n 各语言任一命中即可（支持多语言搜索）
  // 注：以下关键字/别名/名称的小写化均已预计算（见 buildPluginIndex 的 *LC 字段），
  // 不再在每次击键时重新 toLowerCase()。
  const titleHits = (fn: (s: string) => boolean) => idx.titleLc.some(fn)
  if (titleHits(s => s === qLC) && bestScore < 100) { bestScore = 100; bestType = 'exact' }
  if (idx.keywordsLC.some(k => k === qLC) && bestScore < 90) { bestScore = 90; bestType = 'keyword' }
  if (idx.aliasesLC.some(a => a === qLC) && bestScore < 85) { bestScore = 85; bestType = 'alias' }
  if (titleHits(s => s.startsWith(qLC)) && bestScore < 75) { bestScore = 75; bestType = 'prefix' }
  if (idx.keywordsLC.some(k => qLC.startsWith(k + ' ')) && bestScore < 55) { bestScore = 55; bestType = 'kw inline' }
  if (idx.keywordsLC.some(k => k.startsWith(qLC)) && bestScore < 65) { bestScore = 65; bestType = 'kw prefix' }
  if (titleHits(s => s.includes(qLC)) && bestScore < 60) { bestScore = 60; bestType = 'contains' }
  if (idx.keywordsLC.slice(0, 20).some(k => k.includes(qLC)) && bestScore < 40) { bestScore = 40; bestType = 'kw match' }
  if (idx.aliasesLC.some(a => a.includes(qLC)) && bestScore < 50) { bestScore = 50; bestType = 'alias' }
  if (idx.pluginNameLC.includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'plugin' }
  if (idx.pluginDescLC.includes(qLC) && bestScore < 35) { bestScore = 35; bestType = 'desc' }
  if (idx.cmdIdLC.includes(qLC) && bestScore < 30) { bestScore = 30; bestType = 'id' }
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
  // 内容嗅探通道：整段结构化文本（JSON / SQL / URL / JWT …）→ 用格式名命中命令的格式线索。
  // 只在其它规则一条都没命中时才看：用户真在敲命令名时不该被它插一脚。
  if (bestScore === 0) {
    const fmt = detectContentFormat(q)
    if (fmt) {
      const fmtHit = idx.keywordsLC.some(k => k === fmt)
        || idx.aliasesLC.some(a => a.includes(fmt))
        || idx.titleLc.some(s => s.includes(fmt))
      if (fmtHit) {
        bestScore = 72
        bestType = 'content'
        // 内容本身就是待处理的输入：前端插件（打开界面的那类）一律把原文交过去，
        // 由插件自己决定要不要填进编辑器；后端命令只有声明 acceptsInput 才当作参数。
        hitText = q
        if (idx.cmd.acceptsInput) inlineInput = q
      }
    }
  }
  if (bestScore === 0 && qLC.length >= 3) {
    const hasChinese = /[\u4e00-\u9fff]/.test(qLC)
    const threshold = hasChinese ? 3 : 2
    const fuzzyLimit = 20
    if (levenshtein(idx.titleLc[0].slice(0, fuzzyLimit), qLC.slice(0, fuzzyLimit)) <= threshold
        || idx.titleLc.some(s => levenshtein(s.slice(0, fuzzyLimit), qLC.slice(0, fuzzyLimit)) <= threshold)) { bestScore = 10; bestType = 'fuzzy' }
    else if (idx.keywordsLC.some(k => levenshtein(k.slice(0, fuzzyLimit), qLC.slice(0, fuzzyLimit)) <= threshold)) { bestScore = 8; bestType = 'fuzzy' }
  }
  return { score: bestScore, matchType: bestType, inlineInput, hitText }
}

// 命中类型 → i18n key（文案在 i18n/zh-CN.ts 的 命令面板 分组 mt* 键，en-US 同步）
const matchTypeI18nKeys: Record<string, string> = {
  'exact': 'mtExact', 'keyword': 'mtKeyword', 'alias': 'mtAlias', 'prefix': 'mtPrefix',
  'contains': 'mtContains', 'fuzzy': 'mtFuzzy', 'match pattern': 'mtMatchPattern', 'kw inline': 'mtKwInline',
  'kw prefix': 'mtKwPrefix', 'kw match': 'mtKwMatch', 'kw pinyin': 'mtKwPinyin',
  'alias py': 'mtAliasPy', 'pinyin': 'mtPinyin', 'plugin': 'mtPlugin', 'desc': 'mtDesc',
  'id': 'mtId', 'slash': 'mtSlash', 'content': 'mtContent', 'pattern short': 'mtPatternShort',
}

export function usePluginIndex() {
  return { pluginCmdIndex, buildPluginIndex, calcPluginScore, levenshtein, matchTypeI18nKeys, detectContentFormat }
}
