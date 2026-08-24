/**
 * 本地化工具：按当前界面语言选择多语言字段，未命中时回退默认值。
 *
 * 约定：
 * - plugin.json 新增可选字段 name_i18n / description_i18n / title_i18n，
 *   形如 { "zh-CN": "...", "en-US": "..." }；未声明时回退 name / description / title。
 * - 市场 index.json（gen_site.py 输出）同样带 name_i18n / description_i18n。
 * - 语言键匹配规则：精确匹配 locale；无精确匹配时尝试主语言（locale 前两段，如 en-US → en）。
 * - locale 参数兼容 vue-i18n 的 WritableComputedRef（自动取 .value）。
 */

type LocaleLike = string | { value?: string } | undefined

function resolveLocale(locale: LocaleLike): string {
  if (typeof locale === 'string') return locale
  return locale?.value || ''
}

export function localize<T>(value: T, i18nMap?: Record<string, T>, locale?: LocaleLike): T {
  if (!i18nMap || Object.keys(i18nMap).length === 0) return value
  const loc = resolveLocale(locale)
  if (!loc) return value
  // 1) 精确匹配，如 zh-CN / en-US
  if (i18nMap[loc] !== undefined) return i18nMap[loc]
  // 2) 主语言匹配，如 locale=en-US → 查 en；locale=zh-CN → 查 zh
  const primary = loc.split('-')[0].toLowerCase()
  if (primary && i18nMap[primary] !== undefined) return i18nMap[primary]
  return value
}

/** 插件名称本地化 */
export function pluginName(p: { name: string; nameI18n?: Record<string, string> }, locale?: LocaleLike): string {
  return localize(p.name, p.nameI18n, locale)
}

/** 插件描述本地化 */
export function pluginDesc(p: { description?: string; descriptionI18n?: Record<string, string> }, locale?: LocaleLike): string {
  return localize(p.description || '', p.descriptionI18n, locale)
}

/** 插件命令标题本地化 */
export function commandTitle(c: { title: string; titleI18n?: Record<string, string> }, locale?: LocaleLike): string {
  return localize(c.title, c.titleI18n, locale)
}