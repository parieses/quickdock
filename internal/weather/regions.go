package weather

import (
	"strings"
	"unicode/utf8"
)

// cnRegion 把一个中国省级行政区映射到「代表城市」的罗马化检索词。
//
// 为什么需要这张表：Open-Meteo geocoding 只按**地名**做匹配，不认「省」这一级。
// 搜「河南」返回的是黑龙江 / 浙江 / 北京 / 朝鲜 / 四川那些恰好叫「河南」的村和
// 街道（population 全是 0），河南省本身一条都没有；搜「河南省」「郑州市」则是 0
// 结果。用户想要的是省会天气，不是一堆同名村镇。
//
// 为什么存罗马化城市名，而不是中文名或本地经纬度：
//   - 实测中文名索引不全：「海口」只返回云南 / 浙江的同名乡镇，海南海口漏掉，
//     而 "Haikou" 稳定返回人口 300 万的那个；「台北」中文 0 结果，"Taipei" 有。
//   - geocoding 的 name 索引是多语言的：罗马化词 + language=zh 会回中文名，
//     language=en 回英文名，两种界面共用一份表。
//   - 坐标仍由接口给，不在本地维护一份会漂移的经纬度。
//
// aliases 一律小写比较；中文后缀 / 英文后缀由 normalizeRegionKey 统一剥掉。
type cnRegion struct {
	aliases []string // 中文全名 / 简称 / 罗马化名
	query   string   // 发给 geocoding 的代表城市
}

var cnRegions = []cnRegion{
	{[]string{"北京", "京", "beijing", "peking"}, "Beijing"},
	{[]string{"天津", "津", "tianjin"}, "Tianjin"},
	{[]string{"上海", "沪", "申", "shanghai"}, "Shanghai"},
	{[]string{"重庆", "渝", "chongqing"}, "Chongqing"},
	{[]string{"河北", "冀", "hebei"}, "Shijiazhuang"},
	{[]string{"山西", "晋", "shanxi"}, "Taiyuan"},
	{[]string{"辽宁", "辽", "liaoning"}, "Shenyang"},
	{[]string{"吉林", "吉", "jilin"}, "Changchun"},
	{[]string{"黑龙江", "黑", "heilongjiang"}, "Harbin"},
	{[]string{"江苏", "苏", "jiangsu"}, "Nanjing"},
	{[]string{"浙江", "浙", "zhejiang"}, "Hangzhou"},
	{[]string{"安徽", "皖", "anhui"}, "Hefei"},
	{[]string{"福建", "闽", "fujian"}, "Fuzhou"},
	{[]string{"江西", "赣", "jiangxi"}, "Nanchang"},
	{[]string{"山东", "鲁", "shandong"}, "Jinan"},
	{[]string{"河南", "豫", "henan"}, "Zhengzhou"},
	{[]string{"湖北", "鄂", "hubei"}, "Wuhan"},
	{[]string{"湖南", "湘", "hunan"}, "Changsha"},
	{[]string{"广东", "粤", "guangdong"}, "Guangzhou"},
	{[]string{"海南", "琼", "hainan"}, "Haikou"},
	{[]string{"四川", "川", "蜀", "sichuan"}, "Chengdu"},
	{[]string{"贵州", "贵", "黔", "guizhou"}, "Guiyang"},
	{[]string{"云南", "云", "滇", "yunnan"}, "Kunming"},
	{[]string{"陕西", "陕", "秦", "shaanxi"}, "Xi'an"},
	{[]string{"甘肃", "甘", "陇", "gansu"}, "Lanzhou"},
	{[]string{"青海", "青", "qinghai"}, "Xining"},
	{[]string{"台湾", "台", "taiwan"}, "Taipei"},
	{[]string{"内蒙古", "蒙", "inner mongolia", "nei mongol"}, "Hohhot"},
	{[]string{"广西", "桂", "guangxi"}, "Nanning"},
	{[]string{"西藏", "藏", "tibet", "xizang"}, "Lhasa"},
	{[]string{"宁夏", "宁", "ningxia"}, "Yinchuan"},
	{[]string{"新疆", "新", "xinjiang"}, "Urumqi"},
	{[]string{"香港", "港", "hong kong"}, "Hong Kong"},
	{[]string{"澳门", "澳", "macao", "macau"}, "Macao"},
}

// cnCapitalQueries 是代表城市的中文名 → 罗马化检索词，与 cnRegions 的 query 一一对应。
//
// 中文地名索引不全：「海口」只返回云南 / 浙江 / 台湾的同名乡镇（云南那条还带
// 人口，按质量排序会顶到第一条），海南海口整个漏掉；「台北」则 0 结果。
// 用户直接输城市名反而拿到错的那个，比搜省名出垃圾更隐蔽，所以一并走罗马化词。
// key 不带行政后缀（normalizeRegionKey 会削掉「市」）。
var cnCapitalQueries = map[string]string{
	"北京": "Beijing", "天津": "Tianjin", "上海": "Shanghai", "重庆": "Chongqing",
	"石家庄": "Shijiazhuang", "太原": "Taiyuan", "沈阳": "Shenyang", "长春": "Changchun",
	"哈尔滨": "Harbin", "南京": "Nanjing", "杭州": "Hangzhou", "合肥": "Hefei",
	"福州": "Fuzhou", "南昌": "Nanchang", "济南": "Jinan", "郑州": "Zhengzhou",
	"武汉": "Wuhan", "长沙": "Changsha", "广州": "Guangzhou", "海口": "Haikou",
	"成都": "Chengdu", "贵阳": "Guiyang", "昆明": "Kunming", "西安": "Xi'an",
	"兰州": "Lanzhou", "西宁": "Xining", "台北": "Taipei", "呼和浩特": "Hohhot",
	"南宁": "Nanning", "拉萨": "Lhasa", "银川": "Yinchuan", "乌鲁木齐": "Urumqi",
	"香港": "Hong Kong", "澳门": "Macao",
}

// 只有直辖市 / 特别行政区允许带「市」后缀仍按省级行政区处理。
// 否则「吉林市」这类真实城市会被误判成省名，跳到别的省会去。
var cnMunicipalities = map[string]bool{
	"北京": true, "天津": true, "上海": true, "重庆": true,
}

// resolveCityQuery 决定最终发给 geocoding 的检索词。
// strict=true 表示输入是省名或省会名，结果只保留有人口的城市、最多 3 条——
// 这两类查询的词是确定的，同名村镇不该再混进备选。
func resolveCityQuery(raw string) (string, bool) {
	if q, ok := cnCapitalQueries[normalizeRegionKey(raw)]; ok {
		return q, true
	}
	if q, ok := provinceQuery(raw); ok {
		return q, true
	}
	return cityQuery(raw), false
}

// provinceQuery 把省名（含后缀、简称、罗马化名）解析为代表城市的检索词。
func provinceQuery(raw string) (string, bool) {
	key := normalizeRegionKey(raw)
	if key == "" {
		return "", false
	}
	if strings.HasSuffix(strings.TrimSpace(raw), "市") && !cnMunicipalities[key] {
		return "", false
	}
	for _, r := range cnRegions {
		for _, a := range r.aliases {
			if normalizeRegionKey(a) == key {
				return r.query, true
			}
		}
	}
	return "", false
}

// normalizeRegionKey 剥掉行政后缀与分隔符，只用于表查找，不改写实际查询词。
func normalizeRegionKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// 英文后缀按长度从长到短，避免先削掉 "region" 留下半截。
	for _, suf := range []string{
		" special administrative region", " autonomous region", " municipality",
		" province", " city", " sar",
	} {
		s = strings.TrimSuffix(s, suf)
	}
	// 中文后缀同理：民族自治区全称要先于通用「自治区」削掉。
	for _, suf := range []string{
		"维吾尔自治区", "壮族自治区", "回族自治区", "特别行政区", "自治区", "省", "市",
	} {
		s = strings.TrimSuffix(s, suf)
	}
	return strings.NewReplacer(" ", "", "-", "", "'", "", "·", "").Replace(strings.TrimSpace(s))
}

// cityQuery 去掉中文地名的「市」后缀：geocoding 索引的是「郑州」而不是
// 「郑州市」，带后缀直接查会 0 结果。两字地名（如「沙市」）不动，削了就变别处。
func cityQuery(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "市") && utf8.RuneCountInString(s) > 2 {
		return strings.TrimSuffix(s, "市")
	}
	return s
}
