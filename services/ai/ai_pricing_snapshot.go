package ai

// 价目快照：按「provider/model → 单价」补齐内置表的覆盖缺口。
//
// 为什么需要：内置 aiPriceRules 按模型名前缀维护，而实际记录的模型名带渠道前缀
// （`opencode-go/deepseek-v4.1-flash`），前缀一条都对不上——本机 3197 次调用
// （占 94%）因此全被记成「未计价」，界面上就是花费 0。
//
// 数据是**编译期内嵌的快照**（pricing/prices.json），不读任何第三方工具的运行
// 目录：换机器、卸载工具、离线都不影响。代价是价格不会自己变，需要手工更新
// （格式见 pricing/README.md）。
//
// 优先级：内置表命中就用内置，未命中才查快照。反过来不行——内置表给 DeepSeek
// 配了峰时倍数（工作日白天 ×2），而快照里没有这个概念；让快照价覆盖过去，
// 峰时估算会直接砍半。

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed pricing/prices.json
var priceSnapshot []byte

// priceSnapshotDoc 快照文件结构。
//
//	p[provider][model] = [输入, 输出, 缓存命中输入]，单位 USD / 百万 token。
//
// 用数组而不是具名字段：九千多条记录，字段名会让文件凭空大一圈，而它只给机器读。
// Rate 是生成快照时的 USD→CNY 汇率，也是全表唯一的折算依据（见 usdRate）。
type priceSnapshotDoc struct {
	Rate float64                          `json:"rate"`
	At   string                           `json:"at"`
	P    map[string]map[string][3]float64 `json:"p"`
}

var (
	priceOnce sync.Once
	// priceTbl key 是小写 "provider/model"；值是**美元原值**（USD 标记为真），
	// 折算统一交给 estimateCost 的 toCNY。
	priceTbl map[string]ModelPrice
	// priceUSDrate 全表统一的 USD→CNY 汇率，来自快照文件的 rate 字段。
	priceUSDrate float64
)

// initSnapshot 解析内嵌快照，构建查表 map。只跑一次（约 20ms）。
func initSnapshot() {
	priceUSDrate = usdToCNY // 快照坏掉时退回兜底汇率

	var doc priceSnapshotDoc
	if json.Unmarshal(priceSnapshot, &doc) != nil {
		return
	}
	if doc.Rate > 0 {
		priceUSDrate = doc.Rate
	}
	tbl := make(map[string]ModelPrice, len(doc.P)*32)
	for prov, models := range doc.P {
		for id, c := range models {
			// 两边都是 0 的条目（免费/占位）没有信息量，跳过。
			if c[0] <= 0 && c[1] <= 0 {
				continue
			}
			tbl[prov+"/"+strings.ToLower(id)] = ModelPrice{
				Input:       c[0],
				Output:      c[1],
				CachedInput: c[2],
				USD:         true,
			}
		}
	}
	priceTbl = tbl
}

// usdRate 美元折算人民币的汇率。快照文件里的 rate 是唯一来源——
// 手写表里的美元价与快照里的美元价必须按同一个汇率入账，否则同一张卡上
// 「DeepSeek 的老名字按 7.2、新名字按 6.68」，总额就是两种口径相加。
func usdRate() float64 {
	priceOnce.Do(initSnapshot)
	return priceUSDrate
}

// snapshotPrice 查快照价。首次调用时解析，之后纯内存查表。
func snapshotPrice(model string) (ModelPrice, bool) {
	priceOnce.Do(initSnapshot)
	p, ok := priceTbl[strings.ToLower(strings.TrimSpace(model))]
	return p, ok
}

// priceFor 是计价入口：内置表优先，未命中再查快照。
//
// estimateCost 与 cacheSaving 都必须走这里。两处各写一遍「先查内置再查快照」
// 迟早会漏改一处，界面上就会出现「花费算了钱、已省却是 0」这种自相矛盾。
func priceFor(model string) (ModelPrice, bool) {
	if p, ok := lookupPrice(model); ok {
		return p, true
	}
	return snapshotPrice(model)
}
