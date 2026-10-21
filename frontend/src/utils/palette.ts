/* palette.ts — 品牌色 / 图表状态色集中表。
 * 规则：硬编码品牌色、图表色不得散落在组件脚本，统一落此「主题扩展表」。
 * 品牌色为各运行时的官方标识色（Node 绿、PHP 紫…），与明暗主题无关。
 * 图表/状态色供 canvas（无法直接吃 CSS var）与组件以具体值引用。
 */

export const RUNTIME_COLORS: Record<string, string> = {
  php: '#777bb3',
  node: '#539e43',
  go: '#00add8',
  redis: '#d82c20',
  nginx: '#009639',
  git: '#f05032',
  python: '#3776ab',
  caddy: '#1f9e8f',
  apache: '#d52b1e',
  composer: '#885630',
  ffmpeg: '#1f8ac9',
  memcached: '#1170aa',
  mariadb: '#1f5fbf',
  mysql: '#00758f',
  postgresql: '#336791',
  mongodb: '#13aa52',
  mailpit: '#6366f1',
  minio: '#c51d1d',
  frpc: '#3b82f6',
  ftp: '#64748b',
  bun: '#f472b6',
  erlang: '#A90533',
  traefik: '#2393f3',
  rabbitmq: '#ff6600',
  gh: '#24292f',
  mkcert: '#6b7280',
  jdk: '#007396',
}

// 环境页「内置工具组」特纳入口项的标识色（HTTP / 站点 / 端口）
export const SPECIAL_ITEM_COLORS = {
  http: '#4a9eff',
  sites: '#e8a33d',
  ports: '#3ecf8e',
} as const

// 监控页曲线图 canvas 用色：主题无关的具体值（canvas 不支持 CSS var，运行时取此表）
export const CHART_COLORS = {
  bg: '#1a1a2e',
  grid: '#2a2a3e',
  textMuted: '#666',
  textMuted2: '#888',
  textMuted3: '#aaa',
  accent: '#4a9eff',
  ok: '#46b17b',
  danger: '#e84c4c',
  legendBg: 'rgba(0,0,0,0.6)',
  timeoutLine: 'rgba(232,76,76,0.4)',
  timeoutText: 'rgba(232,76,76,0.6)',
} as const
