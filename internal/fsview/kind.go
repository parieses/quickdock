package fsview

import "strings"

// kindByExt 按扩展名给出预览形态。
//
// 刻意不认识 .docx/.xlsx/.pptx：它们要真实内容就得调 Office COM，脆弱且会在用户
// 没装 Office 的机器上静默失败。这类文件落到 KindBinary，前端显示元数据 + 用默认
// 程序打开，比一个渲染错位的半成品诚实。
func kindByExt(ext string) Kind {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".ico", ".avif", ".svg":
		return KindImage
	case ".mp4", ".webm", ".mov", ".m4v", ".mkv", ".avi", ".wmv", ".flv", ".m2ts":
		return KindVideo
	case ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac", ".opus", ".wma":
		return KindAudio
	case ".pdf":
		return KindPDF
	case ".md", ".markdown", ".mdx":
		return KindMarkdown
	case ".zip", ".tar", ".gz", ".tgz", ".bz2", ".7z", ".rar", ".xz", ".cbz", ".cbr":
		return KindArchive
	}
	if lang := langByExt(ext); lang != "" {
		return KindCode
	}
	if isPlainTextExt(ext) {
		return KindText
	}
	return KindBinary
}

// langByExt 把扩展名映射成 highlight.js 的语言标识（前端直接拿来当 class）。
// 返回空串表示「是代码但没有语法高亮」。
func langByExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".py", ".pyw":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".rb":
		return "ruby"
	case ".lua":
		return "lua"
	case ".dart":
		return "dart"
	case ".scala":
		return "scala"
	case ".sh", ".bash", ".zsh", ".fish":
		return "bash"
	case ".ps1", ".psm1":
		return "powershell"
	case ".bat", ".cmd":
		return "dos"
	case ".json", ".jsonc":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "ini"
	case ".ini", ".cfg", ".conf", ".properties":
		return "ini"
	case ".xml", ".plist", ".csproj", ".vcxproj":
		return "xml"
	case ".html", ".htm":
		return "xml"
	case ".css":
		return "css"
	case ".scss", ".sass":
		return "scss"
	case ".less":
		return "less"
	case ".sql":
		return "sql"
	case ".vue":
		return "xml"
	case ".dockerfile":
		return "dockerfile"
	case ".gradle":
		return "groovy"
	case ".proto":
		return "protobuf"
	case ".tex":
		return "latex"
	}
	return ""
}

// isPlainTextExt 纯文本类：没有高亮，但确定是文本。
// 走白名单而不是「不在黑名单就算文本」—— 后者会让 .exe/.dll 被当文本读进来。
func isPlainTextExt(ext string) bool {
	switch ext {
	case "", ".txt", ".log", ".csv", ".tsv", ".env", ".gitignore", ".gitattributes",
		".editorconfig", ".lock", ".sum", ".mod", ".diff", ".patch", ".srt", ".vtt",
		".rst", ".adoc", ".text", ".nfo", ".me", ".license":
		return true
	}
	return false
}

// normalizeLang 把空高亮降级成 plaintext（前端高亮库需要一个明确标识）。
func normalizeLang(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return "plaintext"
	}
	return lang
}
