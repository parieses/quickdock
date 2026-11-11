/**
 * 官方站点地址的唯一来源。
 *
 * 为什么单独一份：「使用说明」（guide.html）在「设置 → 关于」和「工作空间空状态引导」
 * 两处都要露出，硬编码两次的下一站就是两处指向不同版本的文档。改地址只改这里。
 *
 * 只放 https 绝对地址：openExternal / Browser.OpenURL 都只认带协议的完整地址，
 * 相对路径会被静默忽略（点了没反应，最难查的一类）。
 */

/** 首次使用教程（使用说明页） */
export const SITE_GUIDE_URL = 'https://parieses.github.io/quickdock/guide.html'

/** 展示用的裸地址（去掉协议）：沿用「关于」页其它链接只显示域名的写法 */
export const SITE_GUIDE_TEXT = SITE_GUIDE_URL.replace(/^https:\/\//, '')
