package services

import (
	"encoding/base64"
	"strings"
)

// CopyPNGToClipboard 把前端渲染好的 PNG（base64，允许带 data: 前缀）写入系统剪贴板。
//
// 为什么走后端而不是只用 navigator.clipboard：WebView2 里 clipboard.write(ClipboardItem)
// 与 writeText 是同一个坑——静默拒绝（见 utils/clipboard.ts 的说明），表现为
// 「点了复制，粘出来还是旧内容」，而这种失败没有任何信号可查。
//
// 前端仍然优先试浏览器 API（同步、无跨进程开销），失败才调这里兜底。
func (a *AppService) CopyPNGToClipboard(pngBase64 string) *ApiResult {
	raw := strings.TrimSpace(pngBase64)
	if strings.HasPrefix(raw, "data:") {
		if i := strings.Index(raw, ","); i >= 0 {
			raw = raw[i+1:]
		}
	}
	if raw == "" {
		return FailMsg("图像数据为空")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return FailMsg("图像数据无法解码")
	}
	if err := writeClipboardPNG(data); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
