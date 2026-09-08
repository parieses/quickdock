<script setup lang="ts">
/**
 * ConfigEditor —— 配置文件编辑器（零第三方依赖）
 *
 * 实现要点：textarea 透明文字 + 覆盖层（pre）渲染彩色文本，二者字体/行高/padding
 * 必须完全一致才能对齐；滚动时把覆盖层与行号槽按 textarea 的 scrollTop/Left 做
 * translate 同步。搜索匹配在覆盖层里用 <mark> 标注（与语法 span 交叉时按字符粒度开合标签）。
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(
  defineProps<{
    modelValue: string
    readonly?: boolean
  }>(),
  { readonly: false },
)

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'save'): void
}>()

const { t } = useI18n()

const ta = ref<HTMLTextAreaElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const scrollTop = ref(0)
const scrollLeft = ref(0)

const text = computed(() => props.modelValue ?? '')
const lineCount = computed(() => text.value.split('\n').length)

// ---- 修改状态（基线在父组件保存成功后刷新）----
const base = ref(props.modelValue ?? '')
const dirty = computed(() => text.value !== base.value)
function markSaved() {
  base.value = text.value
}
defineExpose({ markSaved, focus })

// ---- 光标行列 ----
const caret = ref({ line: 1, col: 1 })
function updateCaret() {
  const el = ta.value
  if (!el) return
  const before = text.value.slice(0, el.selectionStart).split('\n')
  caret.value = { line: before.length, col: before[before.length - 1].length + 1 }
}

// ---- 搜索 / 替换 ----
const searchOpen = ref(false)
const replaceOpen = ref(false)
const query = ref('')
const replaceText = ref('')
const caseSensitive = ref(false)
const matchIdx = ref(0)

const matches = computed<number[]>(() => {
  const q = query.value
  if (!q) return []
  const hay = caseSensitive.value ? text.value : text.value.toLowerCase()
  const needle = caseSensitive.value ? q : q.toLowerCase()
  const out: number[] = []
  let from = 0
  for (;;) {
    const i = hay.indexOf(needle, from)
    if (i < 0) break
    out.push(i)
    from = i + needle.length
    if (out.length >= 5000) break
  }
  return out
})

// 查询变化后重置到首个匹配
watch([query, caseSensitive], () => {
  matchIdx.value = 0
  if (query.value) nextTick(() => gotoMatch(0))
})

function openSearch() {
  searchOpen.value = true
  nextTick(() => {
    searchInput.value?.focus()
    searchInput.value?.select()
    // 已有选中文本时直接带入搜索框
    const el = ta.value
    if (el) {
      const sel = text.value.slice(el.selectionStart, el.selectionEnd)
      if (sel && !sel.includes('\n')) query.value = sel
    }
  })
}

const LINE_H = 21 // 与 .ce-ta/.ce-hl 的 line-height 保持一致，用于滚动定位

function gotoMatch(delta: number) {
  const ms = matches.value
  if (!ms.length) return
  matchIdx.value = (matchIdx.value + delta + ms.length) % ms.length
  const el = ta.value
  if (!el) return
  const idx = ms[matchIdx.value]
  el.focus()
  el.setSelectionRange(idx, idx + query.value.length)
  // 手动滚动到视图中央（setSelectionRange 不保证滚动）
  const line = text.value.slice(0, idx).split('\n').length - 1
  el.scrollTop = Math.max(0, line * LINE_H - el.clientHeight / 2)
  syncScroll()
  updateCaret()
}

function replaceCurrent() {
  const ms = matches.value
  if (!ms.length) return
  const idx = ms[matchIdx.value]
  const next = text.value.slice(0, idx) + replaceText.value + text.value.slice(idx + query.value.length)
  emit('update:modelValue', next)
  nextTick(() => {
    // 替换后重新定位：保持在同一位置（即下一个匹配处）
    if (matchIdx.value >= matches.value.length) matchIdx.value = 0
    gotoMatch(0)
  })
}

function escapeRegExp(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function replaceAll() {
  const q = query.value
  if (!q) return
  let out: string
  if (caseSensitive.value) {
    out = text.value.split(q).join(replaceText.value)
  } else {
    out = text.value.replace(new RegExp(escapeRegExp(q), 'gi'), replaceText.value)
  }
  emit('update:modelValue', out)
}

// ---- 语法着色：逐字符产出 class ----
function classify(src: string): string[] {
  const cls = new Array<string>(src.length).fill('')
  const n = src.length
  let i = 0
  while (i < n) {
    let e = src.indexOf('\n', i)
    if (e < 0) e = n
    let s = i
    while (s < e && (src[s] === ' ' || src[s] === '\t')) s++
    // 整行注释（# 或行首 ; —— nginx 的语句结束符也是 ;，故只在行首判定）
    if (src[s] === '#' || src[s] === ';') {
      for (let k = s; k < e; k++) cls[k] = 'c-cm'
      i = e + 1
      continue
    }
    let j = s
    let first = true
    while (j < e) {
      const ch = src[j]
      if (ch === ' ' || ch === '\t') {
        j++
        continue
      }
      if (ch === '#') {
        for (let k = j; k < e; k++) cls[k] = 'c-cm'
        break
      }
      if (ch === '"' || ch === "'") {
        let k = j
        for (; k < e && src[k] !== ch; k++) cls[k] = 'c-st'
        if (k < e) cls[k] = 'c-st'
        j = k + 1
        first = false
        continue
      }
      if (ch === '{' || ch === '}') {
        cls[j] = 'c-pu'
        j++
        first = false
        continue
      }
      if (ch >= '0' && ch <= '9') {
        let k = j
        for (; k < e && src[k] >= '0' && src[k] <= '9'; k++) cls[k] = 'c-nu'
        j = k
        first = false
        continue
      }
      if (/[A-Za-z_$.*\\/-]/.test(ch)) {
        let k = j
        for (; k < e && /[A-Za-z0-9_$.:*\\/-]/.test(src[k]); k++) cls[k] = first ? 'c-di' : 'c-pl'
        j = k
        first = false
        continue
      }
      j++
      first = false
    }
    i = e + 1
  }
  return cls
}

const HIGHLIGHT_LIMIT = 200_000 // 超大文件跳过着色，只做转义，避免卡顿

const highlighted = computed(() => {
  const src = text.value
  if (src.length > HIGHLIGHT_LIMIT) {
    return src.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  }
  const cls = classify(src)
  const ms = matches.value
  const qlen = query.value.length
  const mk = new Uint8Array(src.length) // 0=未命中 1=命中 2=当前命中
  for (let m = 0; m < ms.length; m++) {
    const v = m === matchIdx.value ? 2 : 1
    for (let k = ms[m]; k < ms[m] + qlen && k < src.length; k++) mk[k] = v
  }
  let html = ''
  let curCls = ''
  let curMk = 0
  for (let i = 0; i < src.length; i++) {
    const c = cls[i]
    const m = mk[i]
    if (c !== curCls || m !== curMk) {
      if (curMk) html += '</mark>'
      if (curCls) html += '</span>'
      if (c) html += '<span class="' + c + '">'
      if (m) html += '<mark class="' + (m === 2 ? 'm-cur' : 'm-hit') + '">'
      curCls = c
      curMk = m
    }
    const ch = src[i]
    html += ch === '&' ? '&amp;' : ch === '<' ? '&lt;' : ch === '>' ? '&gt;' : ch
  }
  if (curMk) html += '</mark>'
  if (curCls) html += '</span>'
  return html
})

// ---- 滚动同步 ----
const layerStyle = computed(() => ({
  transform: `translate(${-scrollLeft.value}px, ${-scrollTop.value}px)`,
}))
function syncScroll() {
  const el = ta.value
  if (!el) return
  scrollTop.value = el.scrollTop
  scrollLeft.value = el.scrollLeft
}

// ---- 输入 / 快捷键 ----
function onInput(e: Event) {
  const el = e.target as HTMLTextAreaElement
  emit('update:modelValue', el.value)
  updateCaret()
}

function insertAtCursor(s: string) {
  const el = ta.value
  if (!el) return
  const { selectionStart: a, selectionEnd: b } = el
  const next = text.value.slice(0, a) + s + text.value.slice(b)
  emit('update:modelValue', next)
  nextTick(() => {
    el.setSelectionRange(a + s.length, a + s.length)
    updateCaret()
  })
}

function onKeydown(e: KeyboardEvent) {
  const mod = e.ctrlKey || e.metaKey
  if (mod && e.key.toLowerCase() === 's') {
    e.preventDefault()
    emit('save')
    return
  }
  if (mod && e.key.toLowerCase() === 'f') {
    e.preventDefault()
    openSearch()
    if (e.shiftKey) replaceOpen.value = true
    return
  }
  if (e.key === 'Escape' && searchOpen.value) {
    e.preventDefault()
    searchOpen.value = false
    replaceOpen.value = false
    ta.value?.focus()
    return
  }
  if (e.key === 'Tab') {
    e.preventDefault()
    insertAtCursor('  ')
  }
}

function focus() {
  ta.value?.focus()
}

onMounted(() => {
  nextTick(() => {
    ta.value?.focus()
    updateCaret()
  })
})
</script>

<template>
  <div class="ce">
    <!-- 搜索 / 替换栏（Ctrl+F 打开，Esc 关闭） -->
    <div v-if="searchOpen" class="ce-bar">
      <div class="ce-bar-row">
        <input
          ref="searchInput"
          v-model="query"
          class="ce-field"
          :placeholder="t('ceFind')"
          spellcheck="false"
          @keydown.enter.exact.prevent="gotoMatch(1)"
          @keydown.shift.enter.prevent="gotoMatch(-1)"
          @keydown.esc="searchOpen = false"
        />
        <span class="ce-count">
          {{ matches.length ? matchIdx + 1 + ' / ' + matches.length : (query ? t('ceNoMatch') : '') }}
        </span>
        <button class="ce-ibtn" type="button" :title="t('cePrev')" @click="gotoMatch(-1)">↑</button>
        <button class="ce-ibtn" type="button" :title="t('ceNext')" @click="gotoMatch(1)">↓</button>
        <button
          class="ce-ibtn" type="button" :class="{ on: caseSensitive }"
          :title="t('ceCase')" @click="caseSensitive = !caseSensitive"
        >Aa</button>
        <button
          class="ce-ibtn" type="button" :class="{ on: replaceOpen }"
          :title="t('ceReplace')" @click="replaceOpen = !replaceOpen"
        >⇄</button>
        <button class="ce-ibtn" type="button" :title="t('close')" @click="searchOpen = false; replaceOpen = false">×</button>
      </div>
      <div v-if="replaceOpen" class="ce-bar-row">
        <input
          v-model="replaceText"
          class="ce-field"
          :placeholder="t('ceReplaceWith')"
          spellcheck="false"
          @keydown.enter.exact.prevent="replaceCurrent"
          @keydown.esc="replaceOpen = false"
        />
        <button class="ce-sbtn" type="button" @click="replaceCurrent">{{ t('ceReplace') }}</button>
        <button class="ce-sbtn" type="button" @click="replaceAll">{{ t('ceReplaceAll') }}</button>
      </div>
    </div>

    <!-- 编辑区：行号槽 + 高亮层 + 透明 textarea -->
    <div class="ce-main">
      <div class="ce-gutter" aria-hidden="true">
        <div class="ce-gutter-inner" :style="{ transform: `translateY(${-scrollTop}px)` }">
          <div
            v-for="n in lineCount" :key="n"
            class="ce-ln" :class="{ cur: n === caret.line }"
          >{{ n }}</div>
        </div>
      </div>
      <div class="ce-area">
        <pre class="ce-hl"><code :style="layerStyle" v-html="highlighted"></code></pre>
        <textarea
          ref="ta"
          :value="text"
          :readonly="readonly"
          class="ce-ta"
          spellcheck="false"
          wrap="off"
          @input="onInput"
          @scroll="syncScroll"
          @click="updateCaret"
          @keyup="updateCaret"
          @keydown="onKeydown"
        ></textarea>
      </div>
    </div>

    <!-- 状态栏 -->
    <div class="ce-status">
      <span class="ce-st-item">{{ t('ceLineCol', { line: caret.line, col: caret.col }) }}</span>
      <span class="ce-st-sep">·</span>
      <span class="ce-st-item">{{ t('ceLines', { n: lineCount }) }}</span>
      <span class="ce-st-sep">·</span>
      <span class="ce-st-item">{{ text.length }} {{ t('ceChars') }}</span>
      <span v-if="dirty" class="ce-dirty">● {{ t('ceDirty') }}</span>
      <span class="ce-spacer"></span>
      <span class="ce-keys">Ctrl+F {{ t('ceFind') }} · Ctrl+S {{ t('save') }}</span>
    </div>
  </div>
</template>

<style scoped>
.ce {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  margin: 12px 18px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
  overflow: hidden;
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}
.ce:focus-within {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 2px var(--color-accent-bg);
}

/* ---- 搜索栏 ---- */
.ce-bar {
  flex: none;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
  padding: 7px 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ce-bar-row { display: flex; align-items: center; gap: 6px; }
.ce-field {
  flex: 1 1 auto; min-width: 0;
  height: 27px; padding: 0 9px;
  background: var(--color-bg-primary); color: var(--color-text-primary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  font-size: 12.5px; outline: none;
  transition: border-color var(--transition-fast);
}
.ce-field:focus { border-color: var(--color-border-focus); }
.ce-count {
  flex: none; min-width: 52px; text-align: right;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px; color: var(--color-text-muted);
}
.ce-ibtn, .ce-sbtn {
  flex: none; height: 27px; min-width: 27px; padding: 0 7px;
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); border-radius: var(--radius-sm);
  font-size: 12px; cursor: pointer; line-height: 1;
  display: flex; align-items: center; justify-content: center;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.ce-ibtn:hover, .ce-sbtn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ce-ibtn.on { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent); }
.ce-sbtn { padding: 0 11px; font-size: 11.5px; }

/* ---- 编辑主体 ---- */
.ce-main { flex: 1 1 auto; min-height: 0; display: flex; overflow: hidden; }
.ce-gutter {
  flex: none; width: 52px; overflow: hidden;
  background: var(--color-bg-tertiary);
  border-right: 1px solid var(--color-border);
  user-select: none;
}
.ce-gutter-inner { padding: 10px 0; will-change: transform; }
.ce-ln {
  height: 21px; line-height: 21px; padding-right: 10px; text-align: right;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px; color: var(--color-text-disabled);
}
.ce-ln.cur { color: var(--color-accent); background: var(--color-accent-bg); }

.ce-area { position: relative; flex: 1 1 auto; min-width: 0; }

/* 高亮层与 textarea 必须字体/行高/padding 完全一致才能对齐 */
.ce-hl, .ce-ta {
  margin: 0;
  padding: 10px 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
  line-height: 21px;
  letter-spacing: 0;
  tab-size: 2;
  white-space: pre;
  word-wrap: normal;
}
.ce-hl {
  position: absolute; inset: 0;
  overflow: hidden;
  pointer-events: none;
  color: var(--color-text-primary);
}
.ce-hl code { display: block; will-change: transform; font: inherit; }
.ce-ta {
  position: absolute; inset: 0;
  width: 100%; height: 100%;
  border: none; outline: none; resize: none;
  background: transparent;
  color: transparent;               /* 文字由高亮层呈现，只留光标 */
  caret-color: var(--color-text-primary);
  overflow: auto;
}
.ce-ta::selection { background: rgba(56, 139, 253, 0.35); }

/* 语法配色 */
.ce-hl :deep(.c-cm) { color: var(--color-text-disabled); font-style: italic; }
.ce-hl :deep(.c-di) { color: #4aa3ff; font-weight: 500; }
.ce-hl :deep(.c-st) { color: #5ecb8a; }
.ce-hl :deep(.c-nu) { color: #f0a35e; }
.ce-hl :deep(.c-pu) { color: #c792ea; }
.ce-hl :deep(.c-pl) { color: var(--color-text-primary); }

/* 搜索命中 */
.ce-hl :deep(mark) { color: inherit; background: transparent; border-radius: 2px; }
.ce-hl :deep(mark.m-hit) { background: rgba(240, 163, 94, 0.32); }
.ce-hl :deep(mark.m-cur) { background: rgba(240, 163, 94, 0.75); box-shadow: 0 0 0 1px #f0a35e; }

/* ---- 状态栏 ---- */
.ce-status {
  flex: none;
  display: flex; align-items: center; gap: 7px;
  padding: 5px 10px;
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
  font-size: 11px; color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.ce-st-sep { color: var(--color-text-disabled); }
.ce-dirty { color: #f0a35e; }
.ce-spacer { flex: 1 1 auto; }
.ce-keys { color: var(--color-text-disabled); }
</style>
