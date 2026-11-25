// 本文件负责「直接读本机浏览器里的书签」——不必先让用户去浏览器里导出 HTML。
//
// 浏览器导出 HTML 只是一条兼容通道；真正省事的是直接读它们自己的数据文件：
//
//	Chromium 系（Chrome / Edge / Brave / Chromium / Vivaldi / 360 / QQ）
//	    <User Data>/<Profile>/Bookmarks      纯 JSON，未加密，可直读
//	Firefox
//	    <Profiles>/<profile>/places.sqlite   SQLite，需先复制再读（运行中的文件被锁）
//
// 两类来源都归一到 Entry，后面的落库、判重、死链检查完全不必区分来自哪里。
package bookmark

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 来源类型。刻意用短常量而不是字符串直写：service 层要按它分派解析器。
const (
	kindChromium = "chromium"
	kindFirefox  = "firefox"
)

// BrowserSource 一处可直接读取的书签数据。
//
// Key 是回传给后端的唯一标识，形如 `chromium|Chrome|Default`：用户可能同时装了
// 三个 Chromium 内核浏览器、每个又有多个配置，路径反而不适合当前端参数（含盘符、
// 空格、非 ASCII）。Key 只用于二次查找，真正的路径由后端重新扫描时补齐。
type BrowserSource struct {
	Key     string `json:"key"`
	Browser string `json:"browser"`
	Profile string `json:"profile"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
}

// browserRoot 一个浏览器的用户数据根目录候选。
type browserRoot struct {
	browser string
	kind    string
	dir     string
}

// DiscoverSources 扫描本机所有可读的书签来源。
//
// 扫不到不是错误：没装浏览器、刚装还没建配置、或权限被拒都返回空切片，
// 前端据此提示「改用 HTML 文件导入」。这里只做 Stat，不读文件内容，
// 因此每次打开导入弹窗都可以重扫一遍（几十次系统调用）。
func DiscoverSources() []BrowserSource {
	out := make([]BrowserSource, 0, 4)
	for _, root := range browserRoots() {
		switch root.kind {
		case kindChromium:
			out = append(out, chromiumSources(root)...)
		case kindFirefox:
			out = append(out, firefoxSources(root)...)
		}
	}
	// 保持 browserRoots 的声明顺序（Chrome → Edge → …），只按浏览器名稳定排序，
	// 避免 ReadDir 的字典序把「Default」和「Profile 1」打乱成无意义的顺序。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Browser != out[j].Browser {
			return false
		}
		return strings.ToLower(out[i].Profile) < strings.ToLower(out[j].Profile)
	})
	return out
}

// ReadSource 读取一个来源的全部书签。
func ReadSource(src BrowserSource) ([]Entry, error) {
	switch src.Kind {
	case kindChromium:
		return ParseChromiumBookmarks(src.Path)
	case kindFirefox:
		return ParseFirefoxPlaces(src.Path)
	default:
		return nil, fmt.Errorf("不支持的书签来源：%s", src.Kind)
	}
}

// FindSource 按 Key 重新定位来源。
//
// 不把路径当参数：前端只需持有 Key，后端每次重扫一遍（Stat 级成本），
// 顺手也校验了「这个来源现在还在」——用户在弹窗开着的时候卸掉浏览器，
// 导入会得到「来源已失效」而不是一个找不到文件的系统错误。
func FindSource(key string) (BrowserSource, bool) {
	for _, s := range DiscoverSources() {
		if s.Key == key {
			return s, true
		}
	}
	return BrowserSource{}, false
}

// chromiumBookmarkFiles 一个配置目录里可能存放书签的文件名，按优先级排列。
//
//   - Bookmarks        常规本地存储，多数浏览器多数时候就是它
//   - AccountBookmarks 登录账号后的「账号书签」。Chrome 在登录但未开启同步时
//     改用它，本地连 Bookmarks 都不再生成——只剩一个旧的 Bookmarks.bak。
//     实测 Chrome 139 的 Default 目录就是这种情况，只认 Bookmarks 会整个浏览器消失
//   - Bookmarks.bak    上一次写入的备份。前两者都没有时才兜底；它可能比真实书签
//     旧一轮，因此排在最后，宁可导入上一版也不让来源直接消失
var chromiumBookmarkFiles = []string{"Bookmarks", "AccountBookmarks", "Bookmarks.bak"}

// locateChromiumBookmarks 在一个配置目录里按优先级找到真正的书签文件。
func locateChromiumBookmarks(profileDir string) (string, bool) {
	for _, name := range chromiumBookmarkFiles {
		path := filepath.Join(profileDir, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return path, true
		}
	}
	return "", false
}

// chromiumProfileSkip 这些目录是浏览器自己用的，不是用户的配置。
//
// System Profile 存后台服务的书签，Guest Profile 是访客模式的空壳，两个都有
// 一份 Bookmarks 文件，会被当成真实来源列进导入窗口，用户点进去只得到几条
// 与 Chrome 内置功能同名的书签。
var chromiumProfileSkip = map[string]bool{
	"System Profile": true,
	"Guest Profile":  true,
}

// chromiumSources 从一个 User Data 目录里列出所有含书签文件的配置目录。
//
// Crashpad / ShaderCache / GrShaderCache 这类目录也在 User Data 下，但它们没有
// 书签文件，Stat 一次就被筛掉了，不必维护一份黑名单。
func chromiumSources(root browserRoot) []BrowserSource {
	entries, err := os.ReadDir(root.dir)
	if err != nil {
		return nil
	}
	names := chromiumProfileNames(root.dir)
	out := make([]BrowserSource, 0, 2)
	for _, e := range entries {
		if !e.IsDir() || chromiumProfileSkip[e.Name()] {
			continue
		}
		path, ok := locateChromiumBookmarks(filepath.Join(root.dir, e.Name()))
		if !ok {
			continue
		}
		profile := strings.TrimSpace(names[e.Name()])
		if profile == "" {
			profile = e.Name()
		}
		out = append(out, BrowserSource{
			Key:     kindChromium + "|" + root.browser + "|" + e.Name(),
			Browser: root.browser,
			Profile: profile,
			Kind:    kindChromium,
			Path:    path,
		})
	}
	return out
}

// firefoxSources 列出 Firefox 的每个配置目录。
//
// Firefox 的配置目录名是随机的（xxxx.default-release），单看目录名无法分辨
// 「工作」和「个人」，所以优先用 profiles.ini 里用户自己起的名字。
func firefoxSources(root browserRoot) []BrowserSource {
	dirs := []string{filepath.Join(root.dir, "Profiles"), root.dir}
	names := firefoxProfileNames(root.dir)
	seen := map[string]bool{}
	out := make([]BrowserSource, 0, 2)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			full := filepath.Join(dir, e.Name())
			path := filepath.Join(full, "places.sqlite")
			if fi, err := os.Stat(path); err != nil || fi.IsDir() || fi.Size() == 0 {
				continue
			}
			if seen[full] {
				continue
			}
			seen[full] = true
			profile := strings.TrimSpace(names[e.Name()])
			if profile == "" {
				profile = e.Name()
			}
			out = append(out, BrowserSource{
				Key:     kindFirefox + "|" + root.browser + "|" + e.Name(),
				Browser: root.browser,
				Profile: profile,
				Kind:    kindFirefox,
				Path:    path,
			})
		}
	}
	return out
}

// chromiumProfileColors / 这里不读配色，只读名字：Local State 的 info_cache
// 把目录名映射成用户设置的配置名（"默认" / "工作" / "Person 1"）。
func chromiumProfileNames(userDataDir string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
	if err != nil {
		return out
	}
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return out
	}
	for dir, info := range state.Profile.InfoCache {
		out[dir] = info.Name
	}
	return out
}

// firefoxProfileNames 解析 profiles.ini，把目录名映射成用户起的名字。
//
// 文件格式是裸 INI，只需两行：Path=Profiles/xxxx.default-release 与 Name=工作。
// 解析失败一律返回空表——名字只是展示用，退回目录名不影响导入。
func firefoxProfileNames(rootDir string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(rootDir, "profiles.ini"))
	if err != nil {
		return out
	}
	var path, name string
	flush := func() {
		if path != "" && name != "" {
			// Path 可能是 Profiles/xxx 也可能只是 xxx，两种都取基名
			out[filepath.Base(filepath.FromSlash(path))] = name
		}
		path, name = "", ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "path":
			path = strings.TrimSpace(v)
		case "name":
			name = strings.TrimSpace(v)
		}
	}
	flush()
	return out
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
