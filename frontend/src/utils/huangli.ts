/**
 * 每日宜忌（黄历）：完全离线，零网络请求。
 *
 * 为什么单独一个模块、且不并入 utils/lunar.ts：
 * - 农历/节气是**天文数据**，能靠 ICU 日历与太阳视黄经现算，所以 lunar.ts 零依赖；
 * - 宜忌是**传统术数规则**（建除十二神 + 吉神凶煞 + 月建/日干支查表），没有天文
 *   公式可推，只能带规则库。lunar-typescript（MIT，自带类型）把这张表按日期本地算，
 *   比抄一份常量表可靠，也比调第三方 HTTP 接口稳——上一轮刚踩过 WebView2 跟随
 *   系统代理、前端外网请求全超时的坑。
 * - 两套历法**刻意不合并**：日期卡的年月日 / 闰月 / 节气继续由 lunar.ts 提供
 *   （已逐日验证过），这里只产出宜忌与神煞，避免闰月/节气交界那天两处打架。
 *
 * 口径固定为库默认（getDayYi()/getDayJi() 不传 sect，即按农历月建分月）。
 * 库另提供 sect=2（按节气精确分月），两者在节气相邻日会给出**完全不同**的宜忌；
 * 不做成用户开关：口径不唯一是民俗数据本身的性质，摆个切换只会让人觉得数据不可信，
 * 要换就整体换库。UI 里必须标明「民俗参考」。
 *
 * 词条是库返回的固定中文术语（嫁娶、纳采、出火…），属于**数据**不是展示文案：
 * 英文界面保留汉字原文，只有标签与说明走 i18n——硬译成英文反而失真。
 */

import { Solar } from 'lunar-typescript'
import { localISO } from './lunar'
import { log } from './logger'

export interface HuangLiDay {
  /** 本地日期 yyyy-MM-dd */
  date: string
  /** 宜（全量，按库顺序） */
  yi: string[]
  /** 忌（全量） */
  ji: string[]
  /** 日干支，如「甲申」 */
  ganzhi: string
  /** 日纳音，如「泉中水」 */
  naYin: string
  /** 建除十二神，如「开」 */
  zhiXing: string
  /** 冲，如「(戊寅)虎」 */
  chong: string
  /** 煞，如「南」 */
  sha: string
  /** 吉神宜趋 */
  jiShen: string[]
  /** 凶煞宜忌 */
  xiongSha: string[]
  /** 彭祖百忌（天干 / 地支各一句） */
  pengZuGan: string
  pengZuZhi: string
}

// 一天只算一次：组件 computed 依赖 dayInfo.date，跨天自动重算；缓存只防
// 「同一天里组件反复挂载 / 多个调用点」重复解析规则表，不参与节流。
const cache = new Map<string, HuangLiDay | null>()

/** 描述某天的宜忌；库不可用时返回 null，日期卡照常显示农历与节气。 */
export function describeHuangLi(d: Date = new Date()): HuangLiDay | null {
  const date = localISO(d)
  if (cache.has(date)) return cache.get(date) ?? null
  let out: HuangLiDay | null = null
  try {
    const l = Solar.fromYmd(d.getFullYear(), d.getMonth() + 1, d.getDate()).getLunar()
    out = {
      date,
      yi: l.getDayYi(),
      ji: l.getDayJi(),
      ganzhi: l.getDayInGanZhi(),
      naYin: l.getDayNaYin(),
      zhiXing: l.getZhiXing(),
      chong: l.getDayChongDesc(),
      sha: l.getDaySha(),
      jiShen: l.getDayJiShen(),
      xiongSha: l.getDayXiongSha(),
      pengZuGan: l.getPengZuGan(),
      pengZuZhi: l.getPengZuZhi(),
    }
  } catch (e) {
    // 规则库是纯计算，理论上不会抛；真抛了也不能让整张日期卡白掉
    log.warn('dashboard.huangli', e)
  }
  cache.set(date, out)
  if (cache.size > 8) {
    const oldest = cache.keys().next().value
    if (oldest !== undefined) cache.delete(oldest)
  }
  return out
}
