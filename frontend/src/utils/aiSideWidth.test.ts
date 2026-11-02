import { describe, expect, it } from 'vitest'
import {
  AI_MAIN_MIN_W,
  AI_SIDE_MAX_RATIO,
  AI_SIDE_W_MIN,
  clampSideWidth,
  defaultSideWidth,
  parseStoredWidth,
  sideWidthFloor,
} from './aiSideWidth'

describe('sideWidthFloor', () => {
  it('容器够宽时就是 340', () => {
    expect(sideWidthFloor(1600)).toBe(AI_SIDE_W_MIN)
    expect(sideWidthFloor(660)).toBe(AI_SIDE_W_MIN) // 660-320 = 340，刚好够
  })

  it('容器放不下时退到「容器宽 - 主区保底」，优先让主区活下来', () => {
    expect(sideWidthFloor(600)).toBe(280)
    expect(sideWidthFloor(400)).toBe(80)
  })

  it('容器比主区保底还窄时归零，跟 CSS 对 min-width 负值归零的行为一致', () => {
    expect(sideWidthFloor(300)).toBe(0)
    expect(sideWidthFloor(0)).toBe(AI_SIDE_W_MIN) // 容器未知：只守默认下限
    expect(sideWidthFloor(Number.NaN)).toBe(AI_SIDE_W_MIN)
  })
})

describe('clampSideWidth', () => {
  it('正常范围内的值原样返回（四舍五入取整）', () => {
    expect(clampSideWidth(520, 1600)).toBe(520)
    expect(clampSideWidth(520.6, 1600)).toBe(521)
  })

  it('小于下限时抬到 340：再窄标签页标题就没字了', () => {
    expect(clampSideWidth(120, 1600)).toBe(AI_SIDE_W_MIN)
    expect(clampSideWidth(0, 1600)).toBe(AI_SIDE_W_MIN)
    expect(clampSideWidth(-50, 1600)).toBe(AI_SIDE_W_MIN)
  })

  it('两个上限取小：谁先到顶谁生效', () => {
    // 1600 窗口：70% = 1120，容器-主区保底 = 1280 → 比例先到顶，收在 1120
    expect(clampSideWidth(1500, 1600)).toBe(1120)
    // 900 窗口：70% = 630，容器-主区保底 = 580 → 主区保底先到顶，收在 580
    expect(clampSideWidth(800, 900)).toBe(580)
    // 1920 窗口：70% = 1344 < 1600 → 比例上限
    expect(clampSideWidth(5000, 1920)).toBe(1344)
  })

  it('窄容器下下限跟着退，不把主区压死', () => {
    // 700 窗口：下限仍是 340（700-320=380 > 340），主区剩 360
    expect(clampSideWidth(100, 700)).toBe(AI_SIDE_W_MIN)
    // 600 窗口：下限退到 280，主区正好 320
    expect(clampSideWidth(500, 600)).toBe(280)
    expect(clampSideWidth(1, 600)).toBe(280)
    // 400 窗口：下限退到 80
    expect(clampSideWidth(300, 400)).toBe(80)
  })

  it('容器宽度未知（还没挂载 / 被隐藏）时只守下限', () => {
    expect(clampSideWidth(50, 0)).toBe(AI_SIDE_W_MIN)
    expect(clampSideWidth(800, 0)).toBe(800)
    expect(clampSideWidth(800, Number.NaN)).toBe(800)
  })

  it('永不越界：结果落在 [下限, 容器宽] 内，且给主区留够空间', () => {
    for (const cw of [300, 400, 600, 700, 900, 1400, 2000, 3840]) {
      const floor = sideWidthFloor(cw)
      const maxByRatio = cw * AI_SIDE_MAX_RATIO
      for (const want of [-100, 0, 339, 340, 700, 5000]) {
        const got = clampSideWidth(want, cw)
        expect(got).toBeGreaterThanOrEqual(floor)
        expect(got).toBeLessThanOrEqual(cw)
        if (cw >= AI_SIDE_W_MIN + AI_MAIN_MIN_W) {
          // 放得下两者的容器里，主区必须还有 AI_MAIN_MIN_W
          expect(got).toBeLessThanOrEqual(cw - AI_MAIN_MIN_W)
          // 取整会往上抹零点几，用 ceil 比，别被 979.9999 这种值判失败
          expect(got).toBeLessThanOrEqual(Math.ceil(maxByRatio))
        }
      }
    }
  })
})

describe('defaultSideWidth', () => {
  it('按 42% 算，且通过同一套钳制', () => {
    expect(defaultSideWidth(1600)).toBe(Math.round(1600 * 0.42))
    expect(defaultSideWidth(1920)).toBe(Math.round(1920 * 0.42))
    // 窄容器下 42% 小于下限，被抬到 340
    expect(defaultSideWidth(700)).toBeGreaterThanOrEqual(AI_SIDE_W_MIN)
  })

  it('恒在合法区间内', () => {
    for (const cw of [400, 700, 1024, 1600, 2560]) {
      const got = defaultSideWidth(cw)
      expect(got).toBeGreaterThanOrEqual(sideWidthFloor(cw))
      expect(got).toBeLessThanOrEqual(Math.max(sideWidthFloor(cw), cw * AI_SIDE_MAX_RATIO))
    }
  })
})

describe('parseStoredWidth', () => {
  it('正常数字直接转', () => {
    expect(parseStoredWidth('520')).toBe(520)
    expect(parseStoredWidth('520.4')).toBe(520.4)
  })

  it('空值与非法内容一律当没存过', () => {
    expect(parseStoredWidth(null)).toBeNull()
    expect(parseStoredWidth('')).toBeNull()
    expect(parseStoredWidth('abc')).toBeNull()
    expect(parseStoredWidth('NaN')).toBeNull()
    expect(parseStoredWidth('0')).toBeNull()
    expect(parseStoredWidth('-100')).toBeNull()
  })
})
