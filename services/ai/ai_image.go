package ai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/google/uuid"
)

// AI 会话图片的输入、落盘与内容块组装。
//
// 设计前提：**不同模型支持的图片传入方式不同**，且差异是厂商实现细节而非协议保证：
//   - OpenAI / DeepSeek / Kimi / Qwen / Ollama(vllm)：image_url 同时接受公网 URL 与 base64 data URL；
//   - Agnes 2.5 Flash 等：只接受公网可访问的 image_url，传 base64 直接报错；
//   - Gemini 系（OpenAI 兼容端点）：多数只认 base64 inline，不抓任意公网 URL。
//
// 因此能力不猜、不按模型名硬编码推断，而是每个 AI 档案显式声明 imageInput
// （both / url / base64，见 AIProfile.ImageInput），本文件按声明做闸门与组装。

const (
	// aiMaxImagesPerMsg 单条消息最多携带几张图。
	// 各家实际上限：Claude 600、DeepSeek 600，但单条消息挂几十张图既不经济
	// （每张约 1k+ token）也非常见场景，取一个对用户足够、对成本友好的值。
	aiMaxImagesPerMsg = 10

	// aiMaxImageBytes 单张图片字节上限（base64 之前）。
	// 10 MiB 覆盖绝大多数截图与原图；再大应先让用户裁剪——
	// 大图除了上传慢，base64 后还会把请求体撑大 4/3，多数厂商也有请求体上限。
	aiMaxImageBytes = 10 << 20

	// aiMaxImageURLLen 图片 URL 长度上限（对齐 DeepSeek 文档的 8192 字符）。
	aiMaxImageURLLen = 8192

	// aiImageDirName 数据目录下存放 AI 会话图片的子目录名。
	aiImageDirName = "ai-images"

	// aiImageTokenEstimate 摘要压缩时每张图片的 token 估算值。
	// 取各家中位偏保守：DeepSeek 封顶 1024，Claude 约 1600，OpenAI high 档可到 2805。
	aiImageTokenEstimate = 1100
)

// AIChatImage 前端传入的待发送图片（ai/stream 请求体的 images 数组元素）。
type AIChatImage struct {
	Kind    string `json:"kind"`    // "file" | "url"
	URL     string `json:"url"`     // kind=url：公网地址
	DataURL string `json:"dataUrl"` // kind=file：data URL（base64）
	Name    string `json:"name"`    // 原始文件名，仅展示用
}

// AIStoredImage 落库的图片引用（ai_messages.images 的数组元素）。
// 刻意不存 base64 字节：DB 里躺几十兆 base64 会让每次 ListAIMessages 都变慢，
// 且发送时按档案能力重读/直发即可，没有必须缓存字节的理由。
type AIStoredImage struct {
	Kind string `json:"kind"` // "file" | "url"
	URL  string `json:"url"`  // kind=url
	Path string `json:"path"` // kind=file，相对数据目录（如 ai-images/xxx.png）
	Name string `json:"name"` // 展示用文件名
}

// normalizeImageInput 归一化档案的图片输入能力声明。
// 空值（旧档案迁移而来）按 both 处理：OpenAI 兼容端点的主流形态就是两者都收，
// 默认收紧成 url 会让存量用户突然不能发本地图，默认放开顶多是个别端点报错、
// 且报错信息足够明确。
func normalizeImageInput(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "url":
		return "url"
	case "base64":
		return "base64"
	default:
		return "both"
	}
}

// allowsFileImage 该能力声明是否接受本地图片（base64 data URL）。
func allowsFileImage(mode string) bool {
	m := normalizeImageInput(mode)
	return m == "both" || m == "base64"
}

// allowsURLImage 该能力声明是否接受公网图片 URL。
func allowsURLImage(mode string) bool {
	m := normalizeImageInput(mode)
	return m == "both" || m == "url"
}

// prepareChatImages 校验并落盘前端传入的图片，返回可存入 ai_messages.images 的 JSON。
// 无图片时返回 ""。任一图片不合法即整体失败（部分成功的消息会让用户以为都发出去了）。
func (a *AIService) prepareChatImages(cfg AIProfile, in []AIChatImage) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	if len(in) > aiMaxImagesPerMsg {
		return "", fmt.Errorf("单条消息最多携带 %d 张图片", aiMaxImagesPerMsg)
	}
	mode := normalizeImageInput(cfg.ImageInput)
	out := make([]AIStoredImage, 0, len(in))
	for i, im := range in {
		switch im.Kind {
		case "url":
			if !allowsURLImage(mode) {
				return "", fmt.Errorf("当前模型不支持公网图片链接，请改用本地图片")
			}
			u := strings.TrimSpace(im.URL)
			if err := validateImageURL(u); err != nil {
				return "", fmt.Errorf("第 %d 张图片链接无效：%v", i+1, err)
			}
			out = append(out, AIStoredImage{Kind: "url", URL: u, Name: strings.TrimSpace(im.Name)})
		case "file":
			if !allowsFileImage(mode) {
				return "", fmt.Errorf("当前模型仅支持公网图片链接，不支持本地图片（图片需以可访问 URL 传入）")
			}
			mime, data, err := parseImageDataURL(im.DataURL)
			if err != nil {
				return "", fmt.Errorf("第 %d 张图片读取失败：%v", i+1, err)
			}
			rel, err := saveAIImageBytes(data, mime)
			if err != nil {
				return "", fmt.Errorf("第 %d 张图片保存失败：%v", i+1, err)
			}
			name := strings.TrimSpace(im.Name)
			if name == "" {
				name = "image." + extForMIME(mime)
			}
			out = append(out, AIStoredImage{Kind: "file", Path: rel, Name: name})
		default:
			return "", fmt.Errorf("第 %d 张图片类型未知（kind=%q）", i+1, im.Kind)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("图片信息序列化失败: %w", err)
	}
	return string(b), nil
}

// parseStoredImages 解析落库的图片引用 JSON。损坏/为空时返回 nil（不阻断对话）。
func parseStoredImages(raw string) []AIStoredImage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []AIStoredImage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logger.W("[ai] 图片引用 JSON 解析失败，按无图处理: %v", err)
		return nil
	}
	return out
}

// countStoredImages 统计落库图片张数（摘要压缩的 token 预算用）。
func countStoredImages(raw string) int {
	return len(parseStoredImages(raw))
}

// messageContent 把一条消息组装成模型可接受的 content：
// 无图片（或非 user 角色）时保持纯字符串，与历史行为完全一致；
// 有图片时组装为 [text, image_url...] 块数组（OpenAI 兼容形态）。
//
// role 限制不是保守而是协议规定：三家厂商都只接受 user 消息带图
// （system/assistant 带图会 400），所以历史里的图片只在 user 消息上生效。
func (a *AIService) messageContent(cfg AIProfile, role, content, imagesJSON string) interface{} {
	if role != "user" || strings.TrimSpace(imagesJSON) == "" {
		return content
	}
	imgs := parseStoredImages(imagesJSON)
	if len(imgs) == 0 {
		return content
	}
	blocks := make([]map[string]interface{}, 0, len(imgs)+1)
	if text := strings.TrimSpace(content); text != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": content})
	}
	blocks = append(blocks, a.imageContentBlocks(cfg, imgs)...)
	// 全部图片都被能力闸门挡掉时退回纯文本，别发一个只有 text 块的数组
	// （部分端点对单块数组兼容性差，且没有意义）。
	if len(blocks) == 0 || (len(blocks) == 1 && blocks[0]["type"] == "text") {
		return content
	}
	return blocks
}

// imageContentBlocks 按档案能力把落库图片转成 image_url 内容块。
// 能力不匹配的图片跳过并留痕：切档案后历史里的图可能不再可发
// （如从 both 档切到只收 URL 的 Agnes），此时丢图保对话，比整条请求失败好。
func (a *AIService) imageContentBlocks(cfg AIProfile, imgs []AIStoredImage) []map[string]interface{} {
	mode := normalizeImageInput(cfg.ImageInput)
	blocks := make([]map[string]interface{}, 0, len(imgs))
	for _, im := range imgs {
		switch im.Kind {
		case "url":
			if !allowsURLImage(mode) {
				logger.W("[ai] 历史图片为 URL 但当前档案不支持 URL 输入，已跳过")
				continue
			}
			blocks = append(blocks, map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": im.URL},
			})
		case "file":
			if !allowsFileImage(mode) {
				logger.W("[ai] 历史图片为本地文件但当前档案不支持 base64 输入，已跳过")
				continue
			}
			dataURL, err := loadAIImageDataURL(im.Path)
			if err != nil {
				// 文件被用户手动清理/数据目录迁移时会出现：丢图保对话。
				logger.W("[ai] 历史图片读取失败，已跳过 path=%s: %v", im.Path, err)
				continue
			}
			blocks = append(blocks, map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]interface{}{"url": dataURL},
			})
		}
	}
	return blocks
}

// validateImageURL 校验证公网图片 URL：仅 http/https，长度受控。
func validateImageURL(u string) error {
	if u == "" {
		return fmt.Errorf("链接为空")
	}
	if len([]rune(u)) > aiMaxImageURLLen {
		return fmt.Errorf("链接过长（>%d 字符）", aiMaxImageURLLen)
	}
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("仅支持 http/https 链接")
	}
	return nil
}

// parseImageDataURL 解析 data URL，返回 (mime, 原始字节)。
// 同时做两道校验：声明的 MIME 在白名单内，且**文件实际内容（magic bytes）与声明一致**——
// 只信声明的话，把 .exe 改名成 image/png 就能穿过前端校验（DeepSeek 文档亦明确
// "格式由文件实际内容判断，而非文件名或声明的 MIME 类型"，照做）。
func parseImageDataURL(dataURL string) (string, []byte, error) {
	s := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(s, "data:image/") {
		return "", nil, fmt.Errorf("不是图片 data URL")
	}
	idx := strings.Index(s, ";base64,")
	if idx < 0 {
		return "", nil, fmt.Errorf("仅支持 base64 编码的图片")
	}
	mime := strings.ToLower(s[len("data:image/"):idx])
	if !allowedImageMIME(mime) {
		return "", nil, fmt.Errorf("不支持的图片格式：%s（支持 JPEG/PNG/GIF/WebP）", mime)
	}
	raw, err := base64.StdEncoding.DecodeString(s[idx+len(";base64,"):])
	if err != nil {
		return "", nil, fmt.Errorf("base64 解码失败")
	}
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("图片内容为空")
	}
	if len(raw) > aiMaxImageBytes {
		return "", nil, fmt.Errorf("图片超过 %d MiB", aiMaxImageBytes>>20)
	}
	if got := detectImageMIME(raw); got != mime {
		return "", nil, fmt.Errorf("图片内容与声明格式不符（声明 %s，实际 %s）", mime, got)
	}
	return mime, raw, nil
}

// allowedImageMIME 四家厂商交集格式（OpenAI / DeepSeek / Claude 均为此集合；
// Gemini 更宽但多的格式别家不一定收，按交集收口最稳）。
func allowedImageMIME(mime string) bool {
	switch mime {
	case "jpeg", "png", "gif", "webp":
		return true
	}
	return false
}

func extForMIME(mime string) string {
	if mime == "jpeg" {
		return "jpg"
	}
	return mime
}

// detectImageMIME 按 magic bytes 识别图片格式，无法识别返回 ""。
func detectImageMIME(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpeg"
	case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "png"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// aiImageDir 图片目录的绝对路径（不存在则创建）。
func aiImageDir() (string, error) {
	dir := filepath.Join(platform.DefaultDataDir(), aiImageDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建图片目录失败: %w", err)
	}
	return dir, nil
}

// saveAIImageBytes 把图片字节落盘，返回相对数据目录的路径（如 ai-images/xxx.png）。
func saveAIImageBytes(data []byte, mime string) (string, error) {
	dir, err := aiImageDir()
	if err != nil {
		return "", err
	}
	rel := filepath.Join(aiImageDirName, uuid.New().String()+"."+extForMIME(mime))
	abs := filepath.Join(dir, filepath.Base(rel))
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	return rel, nil
}

// loadAIImageDataURL 从盘上重读图片并组装 data URL（发送给模型 / 前端展示共用）。
// relPath 来自本应用生成的 DB 记录，但仍做防逃逸校验：Clean 后必须仍在
// ai-images 目录内，杜绝 "../" 形式的路径把数据目录外文件读走。
func loadAIImageDataURL(relPath string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(relPath))
	if clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("非法图片路径")
	}
	abs := filepath.Join(platform.DefaultDataDir(), clean)
	base := filepath.Join(platform.DefaultDataDir(), aiImageDirName)
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return "", fmt.Errorf("非法图片路径")
	}
	// 上限 +1 字节：文件被换成超大文件时能发现而不是直接撑爆内存。
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("读取图片失败: %w", err)
	}
	defer f.Close()
	buf := make([]byte, aiMaxImageBytes+1)
	n, _ := io.ReadFull(f, buf)
	if n > aiMaxImageBytes {
		return "", fmt.Errorf("图片超过 %d MiB", aiMaxImageBytes>>20)
	}
	data := buf[:n]
	mime := detectImageMIME(data)
	if mime == "" {
		return "", fmt.Errorf("不是受支持的图片格式")
	}
	return "data:image/" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// AILoadImage 读取 AI 会话图片的 data URL，供前端展示历史消息里的图片。
//
// 为什么单独开接口而不是让 AIGetMessages 直接下发 base64：一条历史消息的图片
// 动辄几百 KB，会话消息全量下发时会被图片字节淹没；改为按 path 懒加载，
// 与剪贴板历史的 GetClipboardImageBase64 同一模式（前端 IntersectionObserver 按需拉）。
func (a *AIService) AILoadImage(relPath string) *services.ApiResult {
	dataURL, err := loadAIImageDataURL(relPath)
	if err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(dataURL)
}

// saveUserTurn 落库当前轮用户消息，幂等：若会话最后一条已是「同内容 + 同图片」的
// 用户消息（重试同一轮会出现），则跳过，避免产生无回复的重复用户消息。
//
// 在调用模型【之前】调用：无论后续是成功、上游报错还是被用户打断，历史都不会出现
// 空会话——这是「点进来历史是空的」这个 bug 的直接修复点。
func (a *AIService) saveUserTurn(convID, content, imagesJSON string) error {
	// 只取最后一条而不是 ListAIMessages 之后取末位：那条路每轮发送都要把整个
	// 会话的历史读一遍（含 reasoning 与工具结果），长会话里这一步会越来越慢，
	// 而它要的只是「最后一行是不是我」。
	last, err := a.App.DB.LastAIMessage(convID)
	if err == nil && last != nil {
		if last.Role == "user" && last.Content == content && last.Images == imagesJSON {
			return nil // 本轮用户消息已落库（重试场景），不重复写入
		}
	}
	if imagesJSON != "" {
		_, e := a.App.DB.AddAIMessageImages(convID, "user", content, imagesJSON)
		return e
	}
	_, e := a.App.DB.AddAIMessage(convID, "user", content)
	return e
}

// imageErrorHint 当请求含本地（base64）图片且上游报错疑似与图片相关时，返回一段
// 提示，引导用户把档案的「图片输入方式」改成「仅公网链接」并改用图片 URL。
// 返回空串表示无需额外提示。
//
// 背景：prepareChatImages 只校验档案「声明」的能力（both/url/base64），不校验模型
// 实际能力；base64 被模型拒收时上游报错千奇百怪，统一补一句人话比裸报错有用。
func (a *AIService) imageErrorHint(err error, imgs []AIChatImage) string {
	if err == nil {
		return ""
	}
	hasFile := false
	for _, im := range imgs {
		if im.Kind == "file" {
			hasFile = true
			break
		}
	}
	if !hasFile {
		return ""
	}
	lower := strings.ToLower(err.Error())
	for _, k := range []string{
		"image", "图片", "base64", "invalid", "unsupported",
		"not support", "picture", "multimodal", "vision",
		"dimension", "resolution", "format",
	} {
		if strings.Contains(lower, k) {
			return "（当前模型/接口可能不支持 base64 本地图片，可在该档案「图片输入方式」改为「仅公网链接」并改用图片 URL）"
		}
	}
	return ""
}
