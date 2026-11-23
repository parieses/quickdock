package script

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// exportMeta 导出包里的 script.json。
//
// 为什么不导出 script_envs：变量里通常就是 Cookie / token，而 zip 是要
// 发给别人或传到云盘的。要迁移变量的用户自己复制更安全，也符合直觉。
type exportMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Lang        string `json:"lang"`
	Entry       string `json:"entry"`
	Interpreter string `json:"interpreter"`
	DefaultArgs string `json:"defaultArgs"`
	TimeoutSec  int    `json:"timeoutSec"`
	AutoInstall bool   `json:"autoInstall"`
	Tags        string `json:"tags"`
	ExportedAt  string `json:"exportedAt"`
	App         string `json:"app"`
}

// ExportScript 把脚本目录打包成 zip（含元数据，不含变量）。
func (s *ScriptService) ExportScript(id, destPath string) *services.ApiResult {
	sc, err := s.App.DB.GetScript(id)
	if err != nil {
		return services.Fail(err)
	}
	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		return services.FailMsg("请选择保存位置")
	}
	if !strings.HasSuffix(strings.ToLower(destPath), ".zip") {
		destPath += ".zip"
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return services.Fail(err)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return services.Fail(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	meta := exportMeta{
		Name: sc.Name, Description: sc.Description, Lang: sc.Lang, Entry: sc.Entry,
		Interpreter: sc.Interpreter, DefaultArgs: sc.DefaultArgs, TimeoutSec: sc.TimeoutSec,
		AutoInstall: sc.AutoInstall, Tags: sc.Tags,
		ExportedAt: time.Now().Format(time.RFC3339), App: "quickdock",
	}
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "script.json", Method: zip.Deflate})
	if err != nil {
		return services.Fail(err)
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return services.Fail(err)
	}

	root := s.dir(sc.ID)
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			// 附庸目录不打包：node_modules 能让 zip 从几 KB 涨到几百 MB，
			// 而且解包后本来就要重新 install。
			if name == ".git" || name == "node_modules" || name == "__pycache__" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		// 内部标记不打进包：.qd-trusted 是「本机用户确认过」，.qd-deps-ok 是
		// 「这台机器装过依赖」，两者都只在本机成立，跟着 zip 走只会误导导入方。
		if strings.HasPrefix(info.Name(), ".qd-") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = "files/" + filepath.ToSlash(rel)
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"path": destPath})
}

// ImportScript 从 zip 导入成新脚本。
func (s *ScriptService) ImportScript(zipPath string) *services.ApiResult {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return services.Fail(fmt.Errorf("打开 zip 失败: %w", err))
	}
	defer zr.Close()

	var meta exportMeta
	var files []*zip.File
	for _, f := range zr.File {
		if f.Name == "script.json" {
			rc, err := f.Open()
			if err != nil {
				return services.Fail(err)
			}
			_ = json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&meta)
			rc.Close()
			continue
		}
		if strings.HasPrefix(f.Name, "files/") {
			files = append(files, f)
		}
	}
	if meta.Name == "" {
		meta.Name = strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	}

	created := s.CreateScript(ScriptInput{
		Name: meta.Name, Description: meta.Description, Lang: meta.Lang, Entry: meta.Entry,
		Interpreter: meta.Interpreter, DefaultArgs: meta.DefaultArgs, TimeoutSec: meta.TimeoutSec,
		AutoInstall: meta.AutoInstall, Tags: meta.Tags,
	})
	if created.Code != 0 {
		return created
	}
	sc, _ := created.Data.(*db.Script)

	for _, f := range files {
		rel := strings.TrimPrefix(f.Name, "files/")
		abs, err := s.resolveRel(sc.ID, rel)
		if err != nil {
			// zip slip：包里写着 ../../ 时 resolveRel 会拒绝。整个导入中止并
			// 清掉半成品，避免留下一个「文件缺一半」的脚本。
			_ = s.DeleteScript(sc.ID)
			return services.Fail(fmt.Errorf("压缩包包含非法路径 %s，导入已中止", rel))
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		dst, err := os.Create(abs)
		if err != nil {
			rc.Close()
			continue
		}
		_, copyErr := io.Copy(dst, io.LimitReader(rc, maxFileBytes))
		dst.Close()
		rc.Close()
		if copyErr != nil {
			logger.W("[script] 导入文件失败 %s: %v", rel, copyErr)
		}
	}
	// 导入包自带的入口可能覆盖了模板文件，这里按实际入口再确认一次存在性。
	if abs, err := s.resolveRel(sc.ID, sc.Entry); err != nil {
		_ = abs
	} else if _, statErr := os.Stat(abs); statErr != nil {
		_ = os.WriteFile(abs, []byte(templateFor(sc.Lang, sc.Name)), 0o644)
	}
	// 外面来的脚本默认不可信：CreateScript 会写下 .qd-trusted（自己建的可信），
	// 这里必须删掉，让第一次手动运行前强制确认一次——zip 里完全可能是一行
	// `rm -rf ~`，而「导入」这个动作本身不构成对代码内容的同意。
	_ = os.Remove(filepath.Join(s.dir(sc.ID), trustMarker))
	// 依赖指纹同理：包里的 .qd-deps-ok 可能来自别人的机器，留着会让首次运行
	// 跳过依赖安装，然后在 import 语句上炸掉。
	_ = os.Remove(filepath.Join(s.dir(sc.ID), depsMarker))
	s.emit(EventChanged, map[string]any{"scriptId": sc.ID, "reason": "import"})
	return services.Ok(map[string]any{"script": sc, "needsTrust": true})
}

// DeleteScriptRuns 清空某脚本的运行历史（聚合计数在 scripts 表里，保留不动）。
func (s *ScriptService) DeleteScriptRuns(id string) *services.ApiResult {
	runs, err := s.App.DB.DeleteAllScriptRuns(id)
	if err != nil {
		return services.Fail(err)
	}
	for _, r := range runs {
		if r.LogPath != "" {
			_ = os.Remove(r.LogPath)
		}
	}
	return services.Ok(map[string]any{"deleted": len(runs)})
}
