/* palette.ts — 品牌标识色 → 设计 token 的映射表。
 * 规则：色值只允许出现在 style.css（R4）。这里只保存「id → token 引用」，
 * 组件拿到的是 var(--brand-*)，不再是散落在脚本里的 hex。
 * 品牌色是各运行时的官方标识色（Node 绿、PHP 紫…），与明暗主题无关，
 * 所以 token 定义在 :root 全局段、两套主题共用，不需要各自的变体。
 * 图表颜色不落此表：由 composables/useChartTheme.ts 从 style.css 的 token
 * 经 getComputedStyle 桥接，随明暗主题切换。
 *
 * 为什么没走「脚本只拼 class 名」（SshHostTree.vue 的做法）：这里要按运行时 id
 * 动态查 30+ 个品牌色，再维护一张 id→class 映射表和一份 CSS 规则，等于把同一份
 * 清单写两遍；用 CSS 变量则只有 style.css 一处有值，增删运行时不会漏改。
 */

const brand = (id: string) => `var(--brand-${id})`

export const RUNTIME_COLORS: Record<string, string> = {
  php: brand('php'),
  node: brand('node'),
  go: brand('go'),
  redis: brand('redis'),
  nginx: brand('nginx'),
  git: brand('git'),
  python: brand('python'),
  caddy: brand('caddy'),
  apache: brand('apache'),
  composer: brand('composer'),
  ffmpeg: brand('ffmpeg'),
  memcached: brand('memcached'),
  mariadb: brand('mariadb'),
  mysql: brand('mysql'),
  postgresql: brand('postgresql'),
  mongodb: brand('mongodb'),
  mailpit: brand('mailpit'),
  minio: brand('minio'),
  frpc: brand('frpc'),
  ftp: brand('ftp'),
  bun: brand('bun'),
  erlang: brand('erlang'),
  traefik: brand('traefik'),
  rabbitmq: brand('rabbitmq'),
  gh: brand('gh'),
  mkcert: brand('mkcert'),
  jdk: brand('jdk'),
}

// 环境页「内置工具组」特纳入口项的标识色（HTTP / 站点 / 端口）
export const SPECIAL_ITEM_COLORS = {
  http: brand('special-http'),
  sites: brand('special-sites'),
  ports: brand('special-ports'),
} as const
