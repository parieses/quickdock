<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Cpu, MemoryStick, HardDrive, Network, Monitor, Server, Activity, RefreshCw, ArrowDown, ArrowUp } from '@lucide/vue'
import { GetSystemStatus } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface DiskInfo {
  name: string
  usedGB: number
  totalGB: number
  percent: number
}
interface SystemStatus {
  cpuPercent: number
  memUsedGB: number
  memTotalGB: number
  memPercent: number
  disks: DiskInfo[]
  ips: string[]
  hostname: string
  osVersion: string
  uptimeSeconds: number
  processCount: number
  goVersion: string
  netDownSpeedBps: number
  netUpSpeedBps: number
  netInterface: string
}

const status = ref<SystemStatus | null>(null)
const loading = ref(true)
const refreshing = ref(false)
let timer: number | undefined

async function load(showToast = false) {
  if (showToast) refreshing.value = true
  try {
    const r = unwrap<SystemStatus>(await GetSystemStatus())
    status.value = r
  } catch (e) {
    if (showToast) toast.error(getErrorMessage(e))
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function barColor(p: number): string {
  if (p >= 85) return 'var(--color-danger)'
  if (p >= 60) return 'var(--color-warning)'
  return 'var(--color-success)'
}

function fmtGB(gb: number): string {
  if (gb >= 1024) return (gb / 1024).toFixed(2) + ' TB'
  return gb.toFixed(1) + ' GB'
}

function fmtSpeed(bps: number): string {
  if (bps >= 1024 * 1024) return (bps / 1024 / 1024).toFixed(2) + ' MB/s'
  if (bps >= 1024) return (bps / 1024).toFixed(1) + ' KB/s'
  return bps.toFixed(0) + ' B/s'
}

const memText = computed(() => {
  const s = status.value
  if (!s) return ''
  return `${fmtGB(s.memUsedGB)} / ${fmtGB(s.memTotalGB)}`
})

function fmtUptime(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}天 ${h}小时 ${m}分钟`
  if (h > 0) return `${h}小时 ${m}分钟`
  return `${m}分钟`
}

onMounted(() => {
  load()
  timer = window.setInterval(() => load(true), 3000)
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="ss-page">
    <div class="ss-header">
      <h2 class="ss-title">{{ t('sysStatusTitle') }}</h2>
      <button class="ss-refresh" :class="{ spinning: refreshing }" @click="load(true)" :title="t('refresh')">
        <RefreshCw :size="14" />
      </button>
    </div>

    <div v-if="loading" class="ss-loading">{{ t('loading') }}</div>

    <div v-else-if="status" class="ss-body">
      <!-- 系统信息 -->
      <div class="ss-section-title"><Server :size="14" /> 系统信息</div>
      <div class="ss-sysinfo">
        <div class="ss-sysitem"><span class="ss-syslbl">主机名</span><span class="ss-sysval">{{ status.hostname }}</span></div>
        <div class="ss-sysitem"><span class="ss-syslbl">操作系统</span><span class="ss-sysval">{{ status.osVersion }}</span></div>
        <div class="ss-sysitem"><span class="ss-syslbl">运行时间</span><span class="ss-sysval">{{ fmtUptime(status.uptimeSeconds) }}</span></div>
        <div class="ss-sysitem"><span class="ss-syslbl">运行环境</span><span class="ss-sysval">{{ status.goVersion }}</span></div>
      </div>

      <!-- CPU / 内存 -->
      <div class="ss-cards">
        <div class="ss-card">
          <div class="ss-card-head">
            <Cpu :size="16" class="ss-ic" />
            <span class="ss-card-name">{{ t('statCPU') }}</span>
            <span class="ss-card-val">{{ status.cpuPercent.toFixed(0) }}%</span>
          </div>
          <div class="ss-bar"><span :style="{ width: status.cpuPercent + '%', background: barColor(status.cpuPercent) }" /></div>
          <div class="ss-card-sub">
            <Activity :size="11" /> 进程 {{ status.processCount }} 个
          </div>
        </div>

        <div class="ss-card">
          <div class="ss-card-head">
            <MemoryStick :size="16" class="ss-ic" />
            <span class="ss-card-name">{{ t('statMemory') }}</span>
            <span class="ss-card-val">{{ status.memPercent.toFixed(0) }}%</span>
          </div>
          <div class="ss-bar"><span :style="{ width: status.memPercent + '%', background: barColor(status.memPercent) }" /></div>
          <div class="ss-card-sub">{{ memText }}</div>
        </div>
      </div>

      <!-- 磁盘 -->
      <div class="ss-section-title"><HardDrive :size="14" /> {{ t('statDisk') }}</div>
      <div class="ss-disks">
        <div v-for="d in status.disks" :key="d.name" class="ss-disk">
          <div class="ss-disk-head">
            <span class="ss-disk-name">{{ d.name }}</span>
            <span class="ss-disk-val">{{ d.percent.toFixed(0) }}%</span>
          </div>
          <div class="ss-bar"><span :style="{ width: d.percent + '%', background: barColor(d.percent) }" /></div>
          <div class="ss-disk-sub">{{ fmtGB(d.usedGB) }} / {{ fmtGB(d.totalGB) }}</div>
        </div>
      </div>

      <!-- 网络 -->
      <div class="ss-section-title"><Network :size="14" /> {{ t('statIP') }}</div>
      <div class="ss-ips">
        <span v-for="ip in status.ips" :key="ip" class="ss-ip-badge">{{ ip }}</span>
        <span v-if="status.ips.length === 0" class="ss-ip-none">—</span>
      </div>
      <div v-if="status.netInterface" class="ss-net-speed">
        <div class="ss-net-row"><ArrowDown :size="13" class="net-down" /> {{ fmtSpeed(status.netDownSpeedBps) }}</div>
        <div class="ss-net-row"><ArrowUp :size="13" class="net-up" /> {{ fmtSpeed(status.netUpSpeedBps) }}</div>
        <div class="ss-net-iface">{{ status.netInterface }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ss-page {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  padding: 20px 24px;
  background: var(--color-bg-primary);
}
.ss-header {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}
.ss-title { font-size: 16px; font-weight: 500; color: var(--color-text-primary); margin: 0; }
.ss-refresh {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px;
  border: none; border-radius: 6px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  cursor: pointer; transition: all var(--transition-fast);
}
.ss-refresh:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.ss-refresh.spinning { animation: ss-spin 0.8s linear infinite; }
@keyframes ss-spin { from { transform: rotate(0); } to { transform: rotate(360deg); } }

.ss-loading { padding: 40px; text-align: center; color: var(--color-text-muted); font-size: 13px; }
.ss-body { flex: 1; overflow-y: auto; }

.ss-cards { display: grid; grid-template-columns: repeat(2, 1fr); gap: 12px; margin-bottom: 20px; }
.ss-card {
  background: var(--color-bg-tertiary);
  border-radius: 8px;
  padding: 14px 16px;
  box-shadow: inset 0 0 0 1px var(--color-border);
}
.ss-card-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.ss-ic { color: var(--color-accent); flex-shrink: 0; }
.ss-card-name { font-size: 13px; color: var(--color-text-secondary); flex: 1; }
.ss-card-val { font-size: 15px; font-weight: 600; color: var(--color-text-primary); font-variant-numeric: tabular-nums; }
.ss-card-sub { margin-top: 8px; font-size: 11px; color: var(--color-text-muted); font-family: var(--font-mono, monospace); }

.ss-bar {
  height: 6px;
  border-radius: 3px;
  background: var(--color-bg-active);
  overflow: hidden;
}
.ss-bar > span {
  display: block;
  height: 100%;
  border-radius: 3px;
  transition: width var(--transition-base), background var(--transition-base);
}

.ss-section-title {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase; letter-spacing: 0.5px;
  margin: 4px 0 10px;
}
.ss-section-title svg { color: var(--color-text-disabled); }

.ss-disks { display: flex; flex-direction: column; gap: 12px; margin-bottom: 20px; }
.ss-disk {
  background: var(--color-bg-tertiary);
  border-radius: 8px;
  padding: 12px 14px;
  box-shadow: inset 0 0 0 1px var(--color-border);
}
.ss-disk-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.ss-disk-name { font-size: 13px; color: var(--color-text-primary); font-family: var(--font-mono, monospace); }
.ss-disk-val { font-size: 13px; font-weight: 600; color: var(--color-text-secondary); font-variant-numeric: tabular-nums; }
.ss-disk-sub { margin-top: 8px; font-size: 11px; color: var(--color-text-muted); font-family: var(--font-mono, monospace); }

.ss-ips { display: flex; flex-wrap: wrap; gap: 8px; }
.ss-ip-badge {
  padding: 4px 10px;
  border-radius: 4px;
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: 12px;
  font-family: var(--font-mono, monospace);
  box-shadow: inset 0 0 0 1px var(--color-border);
}
.ss-ip-none { color: var(--color-text-disabled); font-size: 13px; }
.ss-net-speed {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-top: 10px;
  font-size: 13px;
  font-family: var(--font-mono, monospace);
}
.ss-net-row { display: flex; align-items: center; gap: 4px; }
.ss-net-row .net-down { color: var(--color-danger); }
.ss-net-row .net-up { color: var(--color-success, #3fb950); }
.ss-net-iface { color: var(--color-text-secondary); font-size: 11px; font-family: var(--font-sans, sans-serif); }

/* 系统信息行 */
.ss-sysinfo {
  display: grid; grid-template-columns: 1fr 1fr; gap: 6px 24px;
  background: var(--color-bg-tertiary); padding: 12px 14px;
  border-radius: 8px; box-shadow: inset 0 0 0 1px var(--color-border);
  margin-bottom: 16px;
}
.ss-sysitem { display: flex; align-items: center; gap: 8px; min-width: 0; }
.ss-syslbl { font-size: 11px; color: var(--color-text-muted); white-space: nowrap; flex-shrink: 0; }
.ss-sysval { font-size: 12px; color: var(--color-text-primary); font-family: var(--font-mono, monospace); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
