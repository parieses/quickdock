package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"quickdock/internal/sysutil"
)

// python.org 的 Windows「embeddable 便携包」是 QuickDock 安装 Python 的下载源，
// 但它天生缺三样东西：
//
//  1. pip 本身——包内不带 site-packages/pip；
//  2. ensurepip 模块——`python -m ensurepip` 报 "No module named ensurepip"，
//     所以「引导 pip（ensurepip）」这条老路对 embeddable 必然失败；
//  3. venv 模块——同样不在包内（`python -m venv` 同样报 no module）。
//
// 更隐蔽的是第 4 点：embeddable 包里有个 `python3XX._pth` 文件，
// 内容形如
//
//	python315.zip
//	.
//
//	# Uncomment to run site.main() automatically
//	#import site
//
// `import site` 那行**默认是注释掉的**。site 模块负责把 site-packages 挂进
// sys.path，它不运行 → 就算 pip 装进去了，`python -m pip` 依然报 "No module named pip"
// （pip 文件确实躺在 Lib\site-packages 下，只是没人告诉解释器去那儿找）。
//
// 因此本文件实现「embeddable 专用」的两步引导：
//
//	第一步：改写 _pth，取消 import site 的注释（必要时补 site-packages 路径行）；
//	第二步：下载 get-pip.py 并执行，把 pip 装进 site-packages。
//
// 关于 get-pip.py 内嵌的那份 pip wheel：它只用来**执行安装动作**（作为 pip 客户端），
// pip 本体仍要从 PyPI 下载（实测 --no-index 会直接失败：
// "Could not find a version that satisfies the requirement pip"）。
// 故引导必须联网，且源不可用时要能换源重试——见 pipIndexURLs。
//
// 之所以不用 ensurepip：embeddable 包里压根没有这个模块（见上）。

// getPipURLs get-pip.py 的下载地址。
//
// 实测（2026-10）：各国内镜像的 get-pip.py 路径均不可用
// （清华/阿里云返回 403，腾讯云/USTC 返回 404——它们只镜像 PyPI 包，不镜像这个引导脚本），
// 故只有官方 bootstrap.pypa.io 一条可靠路径。
// 真正的源弹性在 pipIndexURLs 那一层：get-pip.py 内嵌的 pip 客户端会按 --index-url
// 去下载 pip 本体，镜像不通时可换一个源重试。
var getPipURLs = []string{
	"https://bootstrap.pypa.io/get-pip.py",
}

// pipIndexURLs 引导 pip 时依次尝试的 PyPI 源。
//
// 实测（2026-10）：清华源与阿里云源对部分网络环境返回 403（可达但拒绝），
// 腾讯云与 USTC 正常，官方 pypi.org 也正常。故不写死单一源，改为依次尝试，
// 首个失败自动换下一个，避免用户卡在「某个镜像挂了」上。
// 顺序：腾讯云（国内最快）→ USTC → 官方 → 清华 → 阿里云。
var pipIndexURLs = []string{
	"https://mirrors.cloud.tencent.com/pypi/simple",
	"https://pypi.mirrors.ustc.edu.cn/simple",
	"https://pypi.org/simple",
	"https://pypi.tuna.tsinghua.edu.cn/simple",
	"https://mirrors.aliyun.com/pypi/simple",
}

// pthFileRe 匹配 embeddable 包的 _pth 文件名（python315._pth / python313._pth）。
var pthFileRe = regexp.MustCompile(`(?i)^python\d+\.(_pth)$`)

// pythonEmbedLayout 描述一个 embeddable Python 版本目录的布局。
type pythonEmbedLayout struct {
	Dir      string // 版本目录
	Exe      string // 解释器绝对路径
	PthFile  string // _pth 文件绝对路径（找不到时为空）
	SitePkgs string // site-packages 目录
	Scripts  string // 脚本目录（pip.exe 等入口）
}

// detectPythonEmbedLayout 探测版本目录是否为 embeddable 布局，并返回各关键路径。
// 非 embeddable（完整版 Python / python-build-standalone）返回 nil。
func detectPythonEmbedLayout(dir string) *pythonEmbedLayout {
	if dir == "" {
		return nil
	}
	exe := firstExe(dir, "python", "python3")
	if exe == "" {
		return nil
	}
	lay := &pythonEmbedLayout{
		Dir:      dir,
		Exe:      exe,
		SitePkgs: filepath.Join(dir, "Lib", "site-packages"),
		Scripts:  filepath.Join(dir, "Scripts"),
	}
	// 在版本目录里找唯一的 _pth 文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		return lay
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if pthFileRe.MatchString(e.Name()) {
			lay.PthFile = filepath.Join(dir, e.Name())
			break
		}
	}
	return lay
}

// fixPthFile 修好 embeddable 的 _pth 文件，让 site-packages 真正生效。
//
// 做两件事：
//  1. 取消 `import site` 的注释（embeddable 默认注释掉，是 pip 装完仍不可用的根因）；
//  2. 若文件里没有 site-packages 路径行，补一行 `Lib\site-packages`
//     （双保险：某些情况下 site 的路径推导与实际目录不一致）。
//
// 返回是否发生了修改（未修改返回 false，便于上层如实汇报）。
func fixPthFile(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	original := string(data)

	lines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	var out []string
	siteEnabled := false
	hasSitePath := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		// 取消 import site 的注释
		if strings.HasPrefix(trimmed, "#") && strings.TrimSpace(strings.TrimPrefix(trimmed, "#")) == "import site" {
			out = append(out, "import site")
			siteEnabled = true
			continue
		}
		if trimmed == "import site" {
			siteEnabled = true
		}
		// 检测是否已有 site-packages 路径行（不区分大小写与分隔符）
		if strings.Contains(strings.ToLower(trimmed), "site-packages") {
			hasSitePath = true
		}
		out = append(out, ln)
	}

	if !siteEnabled {
		// 追加到末尾（_pth 中非注释行按顺序加入 sys.path，import site 必须在路径行之后）
		out = append(out, "import site")
	}
	if !hasSitePath {
		out = append(out, filepath.Join("Lib", "site-packages"))
	}

	// 去掉尾部多余空行，避免每次执行都追加空行
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	result := strings.Join(out, "\n") + "\n"
	if result == original {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(result), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// ensurePythonSitePackages 确保 site-packages 目录存在（embeddable 包解压后没有这个目录）。
func ensurePythonSitePackages(lay *pythonEmbedLayout) error {
	if lay == nil {
		return nil
	}
	if err := os.MkdirAll(lay.SitePkgs, 0755); err != nil {
		return fmt.Errorf("创建 site-packages 目录失败: %w", err)
	}
	return nil
}

// pythonPipInstalled 检测该解释器当前是否已能 `python -m pip`。
func pythonPipInstalled(exe string) bool {
	out := RunVersion(exe, "-m", "pip", "--version")
	return strings.Contains(out, "pip")
}

// pythonModuleAvailable 检测解释器能否找到指定模块（用于 venv / ensurepip 的能力展示）。
func pythonModuleAvailable(exe, module string) bool {
	out := RunVersion(exe, "-c",
		"import importlib.util as u;print('yes' if u.find_spec('"+module+"') else 'no')")
	return strings.Contains(out, "yes")
}

// pythonVenvModuleAvailable 判断该解释器能否 import venv 模块。
// embeddable 包内不含 venv（见本文件头注），故此项为 false；
// 此时靠 virtualenv 补齐建虚拟环境的能力。
func pythonVenvModuleAvailable(exe string) bool {
	return pythonModuleAvailable(exe, "venv")
}

// bootstrapPythonPip 为 embeddable Python 引导 pip。
//
// 完整流程：
//  1. 修 _pth（取消 import site 注释 + 补 site-packages 路径）；
//  2. 确保 site-packages 目录存在；
//  3. 若 pip 已可用则直接返回（幂等，重复执行不重复下载）；
//  4. 下载 get-pip.py 到临时文件；
//  5. 用该解释器执行 get-pip.py，按候选源依次尝试，首个失败自动换下一个。
//
// indexURLs 为候选源列表；传空则用默认的 pipIndexURLs。
// 之所以要多源回退：实测清华源 / 阿里云源对部分网络环境返回 403（可达但拒绝），
// 单源会让引导直接失败，而用户此时既没 pip 也没 venv 可退。
func bootstrapPythonPip(ctx context.Context, lay *pythonEmbedLayout, indexURLs []string) (string, error) {
	if lay == nil {
		return "", fmt.Errorf("未找到 Python 解释器")
	}
	if len(indexURLs) == 0 {
		indexURLs = pipIndexURLs
	}
	var b strings.Builder
	b.WriteString("解释器: " + lay.Exe + "\n")

	// 1) 修 _pth
	changed, err := fixPthFile(lay.PthFile)
	if err != nil {
		return b.String(), fmt.Errorf("修正 _pth 文件失败: %w", err)
	}
	if changed {
		b.WriteString("已启用 site 模块（解除 _pth 中 import site 的注释）\n")
	} else {
		b.WriteString("site 模块已处于启用状态\n")
	}

	// 2) 确保 site-packages 存在
	if err := ensurePythonSitePackages(lay); err != nil {
		return b.String(), err
	}

	// 3) 已装则短路
	if pythonPipInstalled(lay.Exe) {
		b.WriteString("pip 已就绪，无需重复安装\n")
		b.WriteString(pythonPipVersion(lay.Exe))
		return b.String(), nil
	}

	// 4) 下载 get-pip.py
	getPip := filepath.Join(os.TempDir(), "quickdock-get-pip.py")
	defer os.Remove(getPip)
	if err := Download(ctx, getPip, getPipURLs, nil); err != nil {
		return b.String(), fmt.Errorf("下载 get-pip.py 失败: %w", err)
	}
	b.WriteString("已下载 get-pip.py，开始安装 pip…\n")

	// 5) 依次尝试各源。get-pip.py 内部仍需联网下载 pip 本体
	//（它内嵌的那份 pip 只用来执行安装动作），故源不可用时只能换源重试。
	var lastErr error
	var lastOut string
	for i, idx := range indexURLs {
		if i > 0 {
			b.WriteString("上一个源不可用，尝试下一个源…\n")
		}
		out, err := runPythonScript(ctx, lay, getPip, "--index-url", idx)
		if err == nil {
			b.WriteString(out + "\n")
			if pythonPipInstalled(lay.Exe) {
				b.WriteString("使用的源: " + idx + "\n")
				b.WriteString(pythonPipVersion(lay.Exe) + "\n")
				// 顺带补齐 venv：embeddable 无 venv 模块，用 virtualenv 顶上
				if !pythonVenvModuleAvailable(lay.Exe) {
					b.WriteString(installVirtualenv(ctx, lay, indexURLs))
				}
				return b.String(), nil
			}
			lastErr = fmt.Errorf("源 %s 安装后 pip 仍不可用", idx)
			lastOut = out
			continue
		}
		lastErr = err
		lastOut = out
		if ctx.Err() != nil {
			return b.String() + out, fmt.Errorf("执行 get-pip.py 中断: %w", ctx.Err())
		}
	}
	return b.String() + lastOut, fmt.Errorf("所有 PyPI 源均安装失败，最后一次错误: %w", lastErr)
}

// pythonPipVersion 返回 pip 版本行（失败返回提示文案）。
func pythonPipVersion(exe string) string {
	out := RunVersion(exe, "-m", "pip", "--version")
	if strings.TrimSpace(out) == "" {
		return "pip: 未知"
	}
	return out
}

// pythonHasVirtualenv 判断该解释器是否已装 virtualenv。
func pythonHasVirtualenv(exe string) bool {
	out := RunVersion(exe, "-m", "virtualenv", "--version")
	return strings.Contains(out, "virtualenv")
}

// installVirtualenv 为 embeddable 解释器装上 virtualenv，补齐缺失的 venv 能力。
//
// 背景：embeddable 包内不含 venv 模块（`python -m venv` 报 "No module named venv"），
// 且 ensurepip 也不在包内，没法靠标准途径补。而 virtualenv 是纯 pip 包，
// 不依赖解释器内置的 venv 模块，能在 embeddable 上正常创建带 pip 的虚拟环境
// （实测在 Python 3.15 embeddable 上创建成功，seeded pip==26.2.2）。
//
// 失败不致命——venv 只是附加能力，pip 装不上才是硬伤，故此处只返回提示不抛错。
func installVirtualenv(ctx context.Context, lay *pythonEmbedLayout, indexURLs []string) string {
	if lay == nil {
		return ""
	}
	if pythonHasVirtualenv(lay.Exe) {
		return "virtualenv 已安装\n"
	}
	if len(indexURLs) == 0 {
		indexURLs = pipIndexURLs
	}
	var lastErr error
	for _, idx := range indexURLs {
		_, err := runPythonScript(ctx, lay, "-m", "pip", "install", "--index-url", idx, "virtualenv")
		if err == nil {
			return "已安装 virtualenv（源: " + idx + "）\n"
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知错误")
	}
	return "安装 virtualenv 失败: " + lastErr.Error() + "\n"
}

// runPythonScript 用指定解释器执行一个 python 脚本，合并 stdout/stderr。
//
// timeout 为单次执行的上限：引导要依次试多个源，若某个源网络不通，
// 没有上限会一直挂到外层 ctx 超时，后面的源就没机会试了。
func runPythonScript(ctx context.Context, lay *pythonEmbedLayout, args ...string) (string, error) {
	return runPythonScriptTimeout(ctx, lay, 90*time.Second, args...)
}

// runPythonScriptTimeout 同 runPythonScript，但带单次执行时限。
func runPythonScriptTimeout(ctx context.Context, lay *pythonEmbedLayout, timeout time.Duration, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := sysutil.CommandContext(runCtx, lay.Exe, args...)
	// 装包过程会在解释器目录下写 site-packages，工作目录即版本目录
	cmd.Dir = lay.Dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\r\n")
	if err != nil {
		return text, err
	}
	return text, nil
}
