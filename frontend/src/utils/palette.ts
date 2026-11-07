/* palette.ts — 品牌标识色集中表。
 * 规则：硬编码品牌色不得散落在组件脚本，统一落此「主题扩展表」。
 * 品牌色为各运行时的官方标识色（Node 绿、PHP 紫…），与明暗主题无关。
 * 图表颜色不再落此表：由 composables/useChartTheme.ts 从 style.css 的设计
 * token 经 getComputedStyle 桥接，随明暗主题切换。
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

