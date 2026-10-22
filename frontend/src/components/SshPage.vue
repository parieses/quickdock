<script lang="ts" setup>
/**
 * SSH 独立窗口的主页面：左侧会话树 + 顶部标签栏 + 右侧终端（#13）。
 *
 * 窗口是懒创建 + 复用的（关闭只是 Hide），Vue 只在首次加载时 mount 一次，
 * 所以「窗口重新唤到前台」要靠 quickdock:ssh:shown 事件刷新清单，
 * 否则用户改了主机名再打开窗口看到的还是旧数据。
 *
 * 三层分工：
 *   - utils/sshTree.ts   纯函数：扁平列表 → 树、拖拽落点计算
 *   - utils/sshTabs.ts   纯函数：标签数组的开/关/去重
 *   - 本组件             只管编排与副作用（调后端、弹确认）
 *
 * 这样切标签、拖拽排序这些高频交互都能用 vitest 直接覆盖，
 * 不用起 Vue、不用 mock Wails 绑定。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { FolderPlus, Plus, RefreshCw, Server, ShieldCheck } from '@lucide/vue'
import {
  DeleteHost,
  Disconnect,
  ListHosts,
  ListSessions,
  MoveHost,
  SaveHost,
} from '../../bindings/quickdock/services/sshx/sshservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import {
  isSessionRetained,
  type SshHost,
  type SshSessionInfo,
} from '../utils/ssh'
import { buildSshTree, collectSubtreeIds } from '../utils/sshTree'
import {
  closeTab,
  findResumable,
  findTab,
  openOrFocus,
  openTab,
  patchTab,
  syncHost,
  tabSessionId,
  type SshTab,
} from '../utils/sshTabs'
import SshHostTree from './SshHostTree.vue'
import SshHostDialog from './SshHostDialog.vue'
import SshSessionView from './SshSessionView.vue'
import SshTabBar from './SshTabBar.vue'
import SshTrustedHostsDialog from './SshTrustedHostsDialog.vue'
import SshFolderDialog from './SshFolderDialog.vue'
import SftpPanel from './SftpPanel.vue'

const { t } = useI18n()
const { confirm, error: toastError } = useToast()

const hosts = ref<SshHost[]>([])
const loading = ref(true)
const loadErr = ref('')
const sessions = ref<SshSessionInfo[]>([])

const tabs = ref<SshTab[]>([])
const activeId = ref('')
const collapsed = ref<Set<string>>(new Set())

const dialogVisible = ref(false)
const editingHost = ref<SshHost | null>(null)
/** 新建时的父节点：为空表示建在根。 */
const creatingParentId = ref('')
/** 已信任服务器（指纹管理）弹窗 */
const trustedVisible = ref(false)
/** 新建分组弹窗及其父节点 */
const folderVisible = ref(false)
const folderParentId = ref('')
/** SFTP 文件面板：按标签 id 记录谁开着，关某个标签时要一起收掉 */
const sftpTabs = ref<Set<string>>(new Set())

let offShown: (() => void) | null = null

const tree = computed(() => buildSshTree(hosts.value))

const hostCount = computed(() => {
  let n = 0
  for (const h of hosts.value) if (!h.isFolder) n++
  return n
})

/** 有会话在跑（或留着回放）的主机 id，用来在树上打绿点。 */
const busyHostIds = computed(() => {
  const set = new Set<string>()
  for (const s of sessions.value) {
    if (s.hostId) set.add(s.hostId)
  }
  return set
})

/** 多标签下「选中」是多个（每台已打开的主机都算），左树要能同时高亮。 */
const activeHostIds = computed(() => new Set(tabs.value.map(t => t.hostId)))

const activeTab = computed(() => tabs.value.find(t => t.id === activeId.value) || null)

/** 文件面板：给当前活动标签里的那台主机开/关 SFTP 面板。 */
function toggleSftp(tabId: string) {
  const next = new Set(sftpTabs.value)
  if (next.has(tabId)) next.delete(tabId)
  else next.add(tabId)
  sftpTabs.value = next
}
const folderNames = computed(() =>
  hosts.value.filter(h => h.isFolder).map(h => h.name),
)

async function loadSessions() {
  try {
    sessions.value = unwrap<SshSessionInfo[]>(await ListSessions()) || []
  } catch {
    // 会话列表拿不到不该挡住主机清单——那才是这个页面的主功能。
    // 恢复不了只是退回「重新连一次」，不至于让整个页面不可用。
    sessions.value = []
  }
}

/**
 * 从会话列表里给标签找一条可接回的会话。
 *
 * 场景：用户之前开过这台机器的标签又关掉了标签（但 Go 侧会话因异常没清干净），
 * 或者在别的窗口开着这台机器。此时直接 Connect 会多开一个 channel。
 */
function findResumableFor(hostId: string): SshSessionInfo | null {
  return findResumable(sessions.value, hostId, isSessionRetained)
}

async function load() {
  loading.value = true
  loadErr.value = ''
  try {
    hosts.value = unwrap<SshHost[]>(await ListHosts()) || []
    // 已打开的标签要跟着刷新主机名/环境色，否则用户改完配置回到标签
    // 看到的还是打开那一刻的样子。
    tabs.value = syncHost(tabs.value, hosts.value)
  } catch (e: unknown) {
    loadErr.value = getErrorMessage(e)
    hosts.value = []
  } finally {
    loading.value = false
  }
}

// ---- 标签操作 ----

/** 点主机：该主机已有标签就聚焦最近打开的那个，否则新建。 */
async function selectHost(h: SshHost) {
  if (h.isFolder) return
  // 从别的标签切过来时，当前标签可能正跑着命令。但**不弹确认**：
  // 标签栏的意义就是随时切换，切换不销毁会话（SshSessionView 靠 v-show 保留），
  // 所以这里没有任何破坏性后果，弹框只会让多标签变得难用。
  const r = openOrFocus(tabs.value, h, findResumableFor(h.id))
  tabs.value = r.tabs
  activeId.value = r.activeId
}

/** 复制当前会话到新标签：同一台主机再开一个独立会话（共享 TCP，各自一个 channel）。
 *  用于「一个标签跑更新命令、另一个看日志/比对配置」这类场景。 */
function duplicateTab(tabId: string) {
  const tab = findTab(tabs.value, tabId)
  if (!tab) return
  // resume 传 null：强制新开一个 channel，而非接回这条已有会话。
  const r = openTab(tabs.value, tab.host, null)
  tabs.value = r.tabs
  activeId.value = r.activeId
}

async function closeTabById(tabId: string) {
  // 确认必须在动 tabs 之前。
  //
  // 一旦从 tabs 里摘掉这一项，v-for 会立刻卸载对应的 SshSessionView，
  // xterm 随之 dispose——用户刚看的滚动缓冲就没了。此时再点「取消」，
  // 重建出来的是一个全新组件实例：它会重新 doConnect() 开出**第二条**
  // 会话，而原来那条在 Go 侧既没人引用、也没有 UI 入口能关掉（孤儿会话），
  // 连接池的引用计数也永远归不了零。所以先问，问完再拆。
  const target = findTab(tabs.value, tabId)
  if (!target) return

  const sid = tabSessionId(target)
  if (sid) {
    const ok = await confirm(t('sshCloseTabConfirm', { name: target.host.name || target.host.host }))
    if (!ok) return
  }

  const r = closeTab(tabs.value, tabId, activeId.value)
  tabs.value = r.tabs
  activeId.value = r.activeId

  // 标签没了，它的文件面板也就没了宿主主机——不摘掉的话 Set 项会一直累积。
  if (sftpTabs.value.has(tabId)) {
    const next = new Set(sftpTabs.value)
    next.delete(tabId)
    sftpTabs.value = next
  }

  // 必须断开：不断的话 Go 侧那条 session 永远留着，回放缓冲占内存，
  // 连接池的引用计数也减不下去（最后一个引用不归零，TCP 会一直吊着）。
  if (!sid) return
  try {
    unwrap(await Disconnect(sid))
  } catch {
    // 会话可能已经因为断网消失，这里失败不影响用户继续操作
  }
  await loadSessions()
}

/** 标签栏的「+」：回到主机选择。开着标签时不该凭空连一台机器。 */
function onNewTab() {
  // 不清空已有标签——「新建」是加一个，不是换一批。
  // 用户想找另一台机器，点左树就行。
  if (tabs.value.length === 0) return
  activeId.value = ''
}

function markTabBusy(tabId: string, busy: boolean) {
  tabs.value = patchTab(tabs.value, tabId, { busy })
}

/**
 * 记录子组件拿到的 sessionId。
 *
 * 必须存：关标签时父级要断会话，而 sessionId 只存在于子组件的作用域里。
 * 组件断开时会 emit 空串（onDisconnectClick），清掉即可——
 * 留着旧的 id 会让「关标签」去断一条已经不存在的会话。
 */
function onTabSession(tabId: string, sessionId: string) {
  tabs.value = patchTab(tabs.value, tabId, { sessionId })
}

// ---- 主机 CRUD ----

function openNew(parentId = '') {
  editingHost.value = null
  creatingParentId.value = parentId
  dialogVisible.value = true
}

/** 新建文件夹：不走主机表单（那里全是地址/凭据字段，对文件夹无意义）。 */
function createFolder(parentId: string) {
  folderParentId.value = parentId
  folderVisible.value = true
}

async function submitFolder(name: string) {
  folderVisible.value = false
  try {
    unwrap(
      await SaveHost({
        id: '',
        name,
        host: '',
        port: 22,
        user: 'root',
        authType: 'password',
        password: '',
        clearPassword: false,
        keyPath: '',
        keyPassphrase: '',
        legacyAlgorithms: false,
        env: '',
        parentId: folderParentId.value,
        isFolder: true,
        sort: 0,
        remark: '',
      }),
    )
    await load()
  } catch (e) {
    toastError(getErrorMessage(e))
  }
}

function openEdit(h: SshHost) {
  editingHost.value = h
  creatingParentId.value = ''
  dialogVisible.value = true
}

async function onSaved() {
  dialogVisible.value = false
  await load()
}

async function removeHost(h: SshHost) {
  const extra = h.isFolder ? t('sshDeleteFolderExtra') : ''
  const ok = await confirm(t('sshDeleteConfirm', { name: h.name || h.host }) + extra)
  if (!ok) return
  try {
    unwrap(await DeleteHost(h.id))
    // 被删的主机若有打开的标签，一并关掉并断开——否则标签会停在一块死画面上。
    //
    // 删**文件夹**必须展开子树：后端是级联删除的，而文件夹本身不会有标签，
    // 只看 tab.hostId === h.id 会一个都匹配不到。结果就是分组从树上消失了，
    // 但里面主机的标签和会话全留着——重连必然报 ssh_host_not_found，
    // 而且那条 Go 侧会话从此没有任何 UI 入口能关掉。
    const doomed = new Set(h.isFolder ? collectSubtreeIds(tree.value, h.id) : [h.id])
    const affected = tabs.value.filter(tab => doomed.has(tab.hostId))
    for (const tab of affected) {
      const sid = tabSessionId(tab)
      if (sid) {
        try {
          unwrap(await Disconnect(sid))
        } catch {
          // 已经断了，忽略
        }
      }
    }
    if (affected.length) {
      // doomed 装的是 hostId，而 tab.id 是 `${hostId}::${seq}`（见 utils/sshTabs
      // 的 makeTabId），两者不同命名空间——用 doomed.has(tab.id) 永远匹配不上，
      // 标签一个都关不掉。上面 affected 的判定用的是 tab.hostId，这里必须一致。
      tabs.value = tabs.value.filter(tab => !doomed.has(tab.hostId))
      if (!tabs.value.some(tab => tab.id === activeId.value)) {
        activeId.value = tabs.value.length ? tabs.value[tabs.value.length - 1].id : ''
      }
      // 顺带收掉这些标签的文件面板，否则 Set 里会留下永不清理的项。
      // sftpTabs 存的是 tabId 而非 hostId，故这里要遍历 affected 取 tab.id。
      const next = new Set(sftpTabs.value)
      for (const tab of affected) next.delete(tab.id)
      sftpTabs.value = next
    }
    await load()
    await loadSessions()
  } catch (e) {
    toastError(getErrorMessage(e))
  }
}

async function moveHost(payload: { id: string; parentId: string }) {
  try {
    unwrap(await MoveHost(payload.id, payload.parentId))
    await load()
  } catch (e) {
    // 非法落点（拖进自己的子孙）后端会拒绝，前端已经先拦了一道，
    // 能走到这里说明是并发或脏数据。刷新回真实结构，别让树停在错误状态。
    toastError(getErrorMessage(e))
    await load()
  }
}

function toggleFolder(id: string) {
  const next = new Set(collapsed.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  collapsed.value = next
}

// 会话列表变化时树的绿点标记自动更新（busyHostIds 是 computed，模板直接依赖它，
// 不需要额外的 watch —— 加一个空 watch 只会让人误以为这里有副作用逻辑）。

onMounted(async () => {
  // 订阅要放在第一个 await 之前。
  //
  // 挂载期间若发生卸载（窗口 hash 切换、组件被替换），onBeforeUnmount 会先跑，
  // 那时 offShown 还是 null；等 await 返回后再注册，就再没有人对它解绑了。
  offShown = Events.On('quickdock:ssh:shown', () => {
    if (loading.value) return
    void (async () => {
      await load()
      // 会话列表也要刷：窗口隐藏期间可能有会话断开了，只刷主机清单的话
      // 绿点还挂在树上，用户点进去会拿到一条已死的会话去「接回」。
      await loadSessions()
    })()
  })

  await load()
  await loadSessions()
  // 首屏恢复：把最近一条还活着的会话接回一个标签。
  // 只恢复一条而不是全部——窗口一开就是 N 个终端对大多数用户是惊吓。
  const candidate = sessions.value.filter(isSessionRetained).pop()
  if (candidate) {
    const h = hosts.value.find(x => x.id === candidate.hostId && !x.isFolder)
    // 认不出主人（主机被删了）就不恢复：硬接回去用户会看到一个
    // 不认识的终端画面，比重新连一次更难理解。
    if (h) {
      const r = openTab(tabs.value, h, candidate)
      tabs.value = r.tabs
      activeId.value = r.activeId
    }
  }
})

onBeforeUnmount(() => {
  offShown?.()
  offShown = null
})

/** 供调试：当前标签与连接状态。 */
defineExpose({
  tabCount: () => tabs.value.length,
})
</script>

<template>
  <div class="ssh-page">
    <aside class="ssh-side">
      <header class="ssh-side-head">
        <div class="ssh-side-title">
          <span>{{ t('sshHostsTitle') }}</span>
          <em v-if="hostCount" class="ssh-count">{{ hostCount }}</em>
        </div>
        <div class="ssh-side-tools">
          <button class="ssh-tool" :title="t('sshNewFolder')" @click="createFolder('')">
            <FolderPlus :size="14" />
          </button>
          <button class="ssh-tool" :title="t('sshNewHost')" @click="openNew('')">
            <Plus :size="14" />
          </button>
          <button class="ssh-tool" :disabled="loading" :title="t('refresh')" @click="load">
            <RefreshCw :size="13" :class="{ spinning: loading }" />
          </button>
          <button class="ssh-tool" :title="t('sshTrustedTitle')" @click="trustedVisible = true">
            <ShieldCheck :size="14" />
          </button>
        </div>
      </header>

      <div v-if="loadErr" class="ssh-error">{{ loadErr }}</div>

      <SshHostTree
        :tree="tree"
        :loading="loading"
        :load-err="loadErr"
        :busy-host-ids="busyHostIds"
        :active-host-ids="activeHostIds"
        :collapsed="collapsed"
        @select="selectHost"
        @toggle="toggleFolder"
        @edit="openEdit"
        @delete="removeHost"
        @create="openNew"
        @create-folder="createFolder"
        @move="moveHost"
      />
    </aside>

    <main class="ssh-main">
      <SshTabBar
        v-if="tabs.length"
        :tabs="tabs"
        :active-id="activeId"
        :files-open="!!activeId && sftpTabs.has(activeId)"
        @select="(id: string) => (activeId = id)"
        @close="closeTabById"
        @new="onNewTab"
        @files="toggleSftp(activeId)"
        @duplicate="duplicateTab"
      />

      <div class="ssh-stage">
        <!--
          用 v-show 而不是 v-if：切标签不能卸载 SshSessionView，
          卸载会触发它的 onBeforeUnmount → Disconnect，等于「一切标签就断线」。
        -->
        <SshSessionView
          v-for="tab in tabs"
          v-show="tab.id === activeId"
          :key="tab.id"
          :host="tab.host"
          :resume="tab.resume"
          :focused="tab.id === activeId"
          @busy="(v: boolean) => markTabBusy(tab.id, v)"
          @session="(sid: string) => onTabSession(tab.id, sid)"
        />

        <!--
          每个标签一份实例（而不是共用一个）。共用会让「A 开着文件面板 → 切到 B
          → 切回 A」时 A 的目录、滚动位置、选中行全丢——host prop 一变内部
          就重新拉列表了。代价只是多几个组件实例，它们都是 v-show 隐藏的，
          不占可见开销。
        -->
        <template v-for="tab in tabs" :key="`sftp-${tab.id}`">
          <SftpPanel
            v-show="tab.id === activeId && sftpTabs.has(tab.id)"
            class="ssh-sftp"
            :host="tab.host"
            :active="tab.id === activeId && sftpTabs.has(tab.id)"
            @close="toggleSftp(tab.id)"
          />
        </template>

        <div v-if="!activeTab" class="ssh-welcome">
          <Server :size="34" class="ssh-welcome-icon" />
          <p class="ssh-welcome-text">{{ t('sshPickHost') }}</p>
          <p class="ssh-welcome-sub">{{ t('sshHostsDesc') }}</p>
        </div>
      </div>
    </main>

    <SshHostDialog
      :visible="dialogVisible"
      :host="editingHost"
      :parent-id="creatingParentId"
      @saved="onSaved"
      @cancel="dialogVisible = false"
    />

    <SshTrustedHostsDialog v-model:visible="trustedVisible" />

    <SshFolderDialog
      :visible="folderVisible"
      :existing="folderNames"
      @submit="submitFolder"
      @cancel="folderVisible = false"
    />
  </div>
</template>

<style scoped>
.ssh-page {
  display: flex;
  height: 100vh;
  min-height: 0;
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
}

/* ---- 左侧主机树 ---- */
.ssh-side {
  display: flex;
  flex-direction: column;
  flex: 0 0 268px;
  min-height: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-bg-primary);
}

.ssh-side-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-4);
  flex: 0 0 auto;
  padding: var(--space-5) var(--space-5) var(--space-4) var(--space-6);
}

.ssh-side-title {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.ssh-count {
  padding: 0 var(--space-2);
  font-size: 10.5px;
  font-style: normal;
  font-weight: 500;
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-full);
}

.ssh-side-tools {
  display: flex;
  gap: var(--space-2);
}

.ssh-tool {
  display: flex;
  padding: var(--space-2);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}

.ssh-tool:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.ssh-tool:disabled {
  cursor: default;
  opacity: 0.5;
}

.spinning {
  animation: ssh-spin 900ms linear infinite;
}

@keyframes ssh-spin {
  to {
    transform: rotate(360deg);
  }
}

.ssh-error {
  margin: 0 var(--space-5) var(--space-4);
  padding: var(--space-3) var(--space-4);
  font-size: 11.5px;
  color: var(--color-danger);
  background: var(--color-danger-bg);
  border-radius: var(--radius-xs);
}

/* ---- 右侧：标签栏 + 终端 ---- */
.ssh-main {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.ssh-stage {
  position: relative;
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
}

/* v-show 隐藏的标签要彻底移出布局流，否则 display:none 之外的
   定位方式仍会占位；这里统一绝对定位叠放，切标签即切 z 序。 */
.ssh-stage > :deep(.sess) {
  flex: 1 1 auto;
  min-width: 0;
}

/* 文件面板与终端横向分栏：MobaXterm 的经典布局是「左边终端、右边文件」，
   用户传完文件还要在终端里敲解压命令，两者必须同屏可见——
   做成标签切换会逼用户来回翻。 */
.ssh-sftp {
  flex: 0 0 40%;
  min-width: 300px;
  max-width: 620px;
  border-left: 1px solid var(--color-border);
}

.ssh-welcome {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  padding: var(--space-8);
  color: var(--color-text-muted);
  text-align: center;
}

.ssh-welcome-icon {
  color: var(--color-text-disabled);
}

.ssh-welcome-text {
  margin: 0;
  font-size: 12.5px;
  color: var(--color-text-secondary);
}

.ssh-welcome-sub {
  margin: 0;
  max-width: 320px;
  font-size: 11px;
  line-height: 1.6;
}

/* 窗口最小宽度 640：窄屏时压缩侧栏，把宽度让给终端（一行命令别被折成两行） */
@media (max-width: 760px) {
  .ssh-side {
    flex-basis: 212px;
  }
}
</style>
