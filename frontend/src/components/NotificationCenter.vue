<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bell, BellOff, CheckCheck, Trash2, X } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { unwrap } from '../utils/api'
import {
  NotifyList, NotifyUnread, NotifyMarkRead, NotifyMarkAllRead, NotifyDelete, NotifyClear,
} from '../../bindings/quickdock/services/notify/notifyservice'
// 通知条目的形状。刻意在本地声明而不是从 bindings 引：
// Wails 的模型生成只为「出现在方法签名里的具名类型」产出接口，经 ApiResult.Data
// 传回来的结构一律是 any；本文件夹里 LogFile / CrashFile 等也都是本地声明，
// 保持同一种做法，比手写一份"看起来像生成的"镜像类型更好维护。
interface NotifyItem {
  id: string
  kind: string
  level: string
  title: string
  body: string
  refType: string
  refId: string
  read: boolean
  createdTs: number
}

const { t } = useI18n()
const emit = defineEmits<{
  (e: 'navigate', page: string): void
  (e: 'open-settings', page: string): void
}>()

const open = ref(false)
const unread = ref(0)
const items = ref<NotifyItem[]>([])
const filter = ref<'all' | 'unread' | 'monitor' | 'schedule' | 'ai'>('all')
const loading = ref(false)

// 定位：面板挂在侧栏底部，从按钮右侧弹出。
// 用 fixed + 实时量按钮位置，而不是把它塞进侧栏 DOM——侧栏是 overflow:hidden，
// 面板一超出就被裁掉，而且侧栏可以折叠，宽度变化时面板会被挤成一列字。
const btnEl = ref<HTMLElement | null>(null)
const panelEl = ref<HTMLElement | null>(null)
const pos = ref({ left: 0, bottom: 0 })

function placePanel() {
  const el = btnEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  pos.value = {
    left: Math.round(r.right + 10),
    // 面板底部对齐按钮底部：通知条数变化只影响向上生长，不会顶到屏幕外
    bottom: Math.round(window.innerHeight - r.bottom),
  }
}

const shown = computed(() => {
  if (filter.value === 'all') return items.value
  if (filter.value === 'unread') return items.value.filter((n) => !n.read)
  return items.value.filter((n) => n.kind === filter.value)
})

const hasUnread = computed(() => unread.value > 0)

async function load() {
  loading.value = true
  try {
    const [list, n] = await Promise.all([
      NotifyList(200, false),
      NotifyUnread(),
    ])
    items.value = unwrap<{ items: NotifyItem[] }>(list)?.items || []
    unread.value = unwrap<number>(n) || 0
  } catch {
    // 通知列表不是业务闭环的一环：DB 未就绪时安静地留空，不弹错误
  } finally {
    loading.value = false
  }
}

async function toggle() {
  if (open.value) { open.value = false; return }
  placePanel()
  open.value = true
  await load()
  await nextTick()
  placePanel()
}

function close() { open.value = false }

// 点击外部关闭。用 mousedown 而不是 click：
// 面板里的按钮在 click 阶段处理，若监听 click 会在同一次事件里先关面板、
// 按钮的 click 就永远收不到（Vue 的 @click 绑在元素上，DOM 已被移除）。
function onDocDown(e: MouseEvent) {
  if (!open.value) return
  const target = e.target as Node | null
  if (panelEl.value?.contains(target || document.body)) return
  if (btnEl.value?.contains(target || document.body)) return
  close()
}

// 相对时间：通知列表的价值在于「多久之前」，绝对时间戳反而要多读一遍才反应过来。
function relTime(ts: number): string {
  const diff = Date.now() - ts
  if (diff < 60_000) return t('ncJustNow')
  if (diff < 3_600_000) return t('ncMinutesAgo', { n: Math.floor(diff / 60_000) })
  if (diff < 86_400_000) return t('ncHoursAgo', { n: Math.floor(diff / 3_600_000) })
  return t('ncDaysAgo', { n: Math.floor(diff / 86_400_000) })
}

const kindLabel = (k: string) => {
  switch (k) {
    case 'monitor': return t('ncKindMonitor')
    case 'schedule': return t('ncKindSchedule')
    case 'ai': return t('ncKindAi')
    default: return t('ncKindSystem')
  }
}

async function activate(n: NotifyItem) {
  if (!n.read) {
    n.read = true
    unread.value = Math.max(0, unread.value - 1)
    try { unwrap(await NotifyMarkRead(n.id)) } catch { /* ignore */ }
  }
  close()
  // 跳转：refType 决定去哪。认不出的类型就只标记已读——
  // 硬跳一个不存在的页面比不跳更糟（用户会以为功能坏了）。
  if (n.refType === 'monitor') emit('navigate', 'monitor')
  else if (n.refType === 'schedule') emit('navigate', 'schedule')
  else if (n.refType === 'ai') emit('navigate', 'ai')
  else if (n.refType.startsWith('page:')) emit('navigate', n.refType.slice(5))
  else if (n.refType.startsWith('settings:')) emit('open-settings', n.refType.slice(9))
}

async function markAll() {
  try {
    unwrap(await NotifyMarkAllRead())
    items.value = items.value.map((n) => ({ ...n, read: true }))
    unread.value = 0
  } catch { /* ignore */ }
}

// 清空点两次：不可逆，但也不珍贵，不值得弹模态框打断。
const clearArmed = ref(false)
let clearTimer: ReturnType<typeof setTimeout> | null = null
async function clearAll() {
  if (!clearArmed.value) {
    clearArmed.value = true
    if (clearTimer) clearTimeout(clearTimer)
    clearTimer = setTimeout(() => { clearArmed.value = false }, 3000)
    return
  }
  if (clearTimer) clearTimeout(clearTimer)
  clearArmed.value = false
  try {
    unwrap(await NotifyClear())
    items.value = []
    unread.value = 0
  } catch { /* ignore */ }
}

async function removeOne(n: NotifyItem) {
  try {
    unwrap(await NotifyDelete(n.id))
    items.value = items.value.filter((x) => x.id !== n.id)
    if (!n.read) unread.value = Math.max(0, unread.value - 1)
  } catch { /* ignore */ }
}

// 新通知到达：后端每次写入都会发 notify:new，这里就地插入，
// 不等下一次轮询——否则「刚发生的事」要等十几秒才出现在铃铛上。
let offNew: (() => void) | null = null
onMounted(() => {
  offNew = Events.On('notify:new', (ev: any) => {
    const n = (ev?.data ?? ev) as NotifyItem
    if (!n || !n.id) return
    if (items.value.some((x) => x.id === n.id)) return
    items.value = [n, ...items.value].slice(0, 200)
    if (!n.read) unread.value += 1
  })
  void load()
  window.addEventListener('resize', placePanel)
  document.addEventListener('mousedown', onDocDown, true)
})

onUnmounted(() => {
  if (offNew) { offNew(); offNew = null }
  window.removeEventListener('resize', placePanel)
  document.removeEventListener('mousedown', onDocDown, true)
  if (clearTimer) clearTimeout(clearTimer)
})

// 面板打开时锁住滚动位置无意义（面板自身滚动），但窗口缩放要重新贴合按钮
watch(open, (v) => { if (v) void nextTick(placePanel) })
</script>

<template>
  <div class="nc-root">
    <button
      ref="btnEl"
      class="nc-bell"
      :class="{ active: open }"
      :title="hasUnread ? t('ncBadgeTitle', { n: unread }) : t('ncTitle')"
      @click="toggle"
    >
      <Bell :size="16" />
      <span v-if="hasUnread" class="nc-badge">{{ unread > 99 ? '99+' : unread }}</span>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="panelEl"
        class="nc-panel"
        :style="{ left: pos.left + 'px', bottom: pos.bottom + 'px' }"
      >
        <div class="nc-head">
          <span class="nc-head-title">{{ t('ncTitle') }}</span>
          <span v-if="hasUnread" class="nc-head-count">{{ unread }}</span>
          <button
            class="nc-icon-btn"
            :disabled="!hasUnread"
            :title="t('ncMarkAll')"
            @click="markAll"
          >
            <CheckCheck :size="14" />
          </button>
          <button
            class="nc-icon-btn"
            :class="{ armed: clearArmed }"
            :title="clearArmed ? t('ncClearConfirm') : t('ncClear')"
            @click="clearAll"
          >
            <Trash2 :size="14" />
          </button>
          <button class="nc-icon-btn" :title="t('close')" @click="close">
            <X :size="14" />
          </button>
        </div>

        <div class="nc-filters">
          <button
            v-for="f in (['all', 'unread', 'monitor', 'schedule', 'ai'] as const)"
            :key="f"
            class="nc-chip"
            :class="{ on: filter === f }"
            @click="filter = f"
          >{{ f === 'all' ? t('ncAll') : f === 'unread' ? t('ncUnread') : kindLabel(f) }}</button>
        </div>

        <div class="nc-list">
          <button
            v-for="n in shown"
            :key="n.id"
            class="nc-item"
            :class="[`lv-${n.level}`, { unread: !n.read }]"
            @click="activate(n)"
          >
            <span class="nc-item-bar" />
            <span class="nc-item-main">
              <span class="nc-item-top">
                <span class="nc-kind">{{ kindLabel(n.kind) }}</span>
                <span class="nc-time">{{ relTime(n.createdTs) }}</span>
                <span v-if="!n.read" class="nc-dot" />
              </span>
              <span class="nc-item-title">{{ n.title }}</span>
              <span v-if="n.body" class="nc-item-body">{{ n.body }}</span>
            </span>
            <span
              class="nc-del"
              :title="t('ncDelete')"
              @click.stop="removeOne(n)"
            ><X :size="12" /></span>
          </button>

          <div v-if="!shown.length" class="nc-empty">
            <BellOff :size="22" />
            <span>{{ filter === 'unread' ? t('ncEmptyUnread') : t('ncEmpty') }}</span>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* 铃铛按钮与侧栏的「设置」按钮同构：同样的高度与字形，
   两个入口并排时不至于一个粗一个细。 */
.nc-root { position: relative; flex: 0 0 auto; }
/* 只给图标、不放文字：footer 里已有「设置」与「收起」两个占位，
   再塞第三个带文字的按钮，210px 宽会被压成一排看不清的小字。
   未读数做角标压在图标右上角——这是各平台通知入口的通用形态，无需解释。 */
.nc-bell {
  position: relative;
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; padding: 0;
  border: none; border-radius: var(--radius-md);
  background: transparent; color: var(--color-text-secondary);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.nc-bell:hover, .nc-bell.active { background: var(--color-bg-hover); color: var(--color-text-primary); }
.nc-bell:focus-visible { outline: 2px solid var(--color-border-focus); outline-offset: 1px; }

.nc-badge {
  position: absolute; top: -1px; right: -3px;
  min-width: 14px; height: 14px; padding: 0 3px;
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: 7px;
  /* 用主色而不是红色：红色留给真正的故障（监控宕机）。
     通知里大部分是「完成任务」，全用红色会和角标的语义脱节。 */
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: 9.5px; font-weight: 700; line-height: 1;
  font-variant-numeric: tabular-nums;
  box-shadow: 0 0 0 1.5px var(--color-bg-secondary);
}

/* Teleport 到 body，scoped 的 data 属性会跟着走（Vue 会把 scope id 打在
   编译出的节点上），所以这里不需要 :deep 或全局类。 */
.nc-panel {
  position: fixed; z-index: 600;
  width: 360px; max-height: 460px;
  display: flex; flex-direction: column;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
  overflow: hidden;
}

.nc-head {
  display: flex; align-items: center; gap: 6px;
  padding: 10px 10px 8px 14px;
  border-bottom: 1px solid var(--color-border);
}
.nc-head-title { font-size: 12.5px; font-weight: 600; color: var(--color-text-primary); }
.nc-head-count {
  min-width: 17px; height: 17px; padding: 0 5px; margin-right: auto;
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: 9px; background: var(--color-danger-bg); color: var(--color-danger-text);
  font-size: 10px; font-weight: 700; font-variant-numeric: tabular-nums;
}
.nc-icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; padding: 0;
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-muted); cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.nc-icon-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.nc-icon-btn:disabled { opacity: 0.35; cursor: default; }
.nc-icon-btn.armed { color: var(--color-danger-text); background: var(--color-danger-bg); }

.nc-filters { display: flex; gap: 4px; padding: 8px 12px; flex-wrap: wrap; }
.nc-chip {
  height: 22px; padding: 0 9px;
  border: 1px solid var(--color-border); border-radius: 11px;
  background: transparent; color: var(--color-text-muted);
  font-family: inherit; font-size: 11px; cursor: pointer;
  transition: all var(--transition-fast);
}
.nc-chip:hover { color: var(--color-text-primary); border-color: var(--color-accent-border); }
.nc-chip.on {
  background: var(--color-accent-bg); border-color: var(--color-accent-border);
  color: var(--color-accent);
}

.nc-list { flex: 1; overflow-y: auto; padding: 0 8px 8px; }

.nc-item {
  position: relative;
  display: flex; align-items: stretch; gap: 0;
  width: 100%; padding: 9px 8px 9px 12px; margin-bottom: 2px;
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: inherit; text-align: left; cursor: pointer;
  transition: background var(--transition-fast);
}
.nc-item:hover { background: var(--color-bg-hover); }
/* 未读用「左侧竖条 + 标题加重」，不用整行底色：
   整行变色时一屏 8 条全是未读，看着像全是错误。 */
.nc-item-bar {
  position: absolute; left: 3px; top: 10px; bottom: 10px;
  width: 2px; border-radius: 1px; background: transparent;
}
.nc-item.unread .nc-item-bar { background: var(--color-accent); }
.nc-item.lv-error .nc-item-bar { background: var(--color-danger); }
.nc-item.lv-warn .nc-item-bar { background: var(--color-warning); }
.nc-item.lv-success .nc-item-bar { background: var(--color-success); }

.nc-item-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.nc-item-top { display: flex; align-items: center; gap: 6px; }
.nc-kind {
  font-size: 10px; padding: 0 5px; height: 15px; line-height: 15px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
.nc-time { font-size: 10px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.nc-dot {
  margin-left: auto; width: 6px; height: 6px; border-radius: 50%;
  background: var(--color-accent);
}
.nc-item-title {
  font-size: 12px; color: var(--color-text-secondary); line-height: 1.45;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.nc-item.unread .nc-item-title { color: var(--color-text-primary); font-weight: 500; }
.nc-item-body {
  font-size: 11px; color: var(--color-text-disabled); line-height: 1.5;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden; white-space: pre-wrap;
}

.nc-del {
  flex-shrink: 0; align-self: center;
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border-radius: var(--radius-sm);
  color: var(--color-text-disabled); opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.nc-item:hover .nc-del { opacity: 1; }
.nc-del:hover { color: var(--color-danger-text); background: var(--color-danger-bg); }

.nc-empty {
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  padding: 44px 0; color: var(--color-text-disabled); font-size: 11.5px;
}
</style>
