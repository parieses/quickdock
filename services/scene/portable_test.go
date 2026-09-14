package scene

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quickdock/internal/db"
	"quickdock/services"
)

// newTestService 造一个只带数据库的 SceneService。
// Env 留 nil 是刻意的：导入时目标端「没装某运行时」正是最常见场景，
// 用它来验证环境绑定会走 skippedEnv 而不是让整包导入失败。
func newTestService(t *testing.T) (*SceneService, *db.Database) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewSceneService(&services.AppService{DB: d}), d
}

func mustOK(t *testing.T, r *services.ApiResult, what string) interface{} {
	t.Helper()
	if r == nil {
		t.Fatalf("%s: 返回 nil", what)
	}
	if r.Code != 0 {
		t.Fatalf("%s: 期望成功，实际失败: %s", what, r.Msg)
	}
	return r.Data
}

// TestSceneExportImportRoundTrip 导出的场景重新导入后，声明性字段必须逐项一致。
// 这是「声明式」这个说法的底线：只要有一条字段在往返中丢失，文件就不是可信任的
// 项目描述文件，用户放进仓库后别人导入得到的是残缺场景。
func TestSceneExportImportRoundTrip(t *testing.T) {
	svc, d := newTestService(t)

	ws, err := d.CreateWorkspace("测试空间")
	if err != nil {
		t.Fatalf("建工作空间失败: %v", err)
	}
	sc, err := d.CreateScene(ws.ID, "后端开发", "通用")
	if err != nil {
		t.Fatalf("建场景失败: %v", err)
	}
	if err := d.UpdateScene(sc.ID, map[string]interface{}{
		"description": "后端日常", "icon": "server", "color": "#cfa93f",
		"env": `[{"runtime":"mysql","version":"8.4.3"}]`,
	}); err != nil {
		t.Fatalf("更新场景失败: %v", err)
	}
	c, err := d.CreateCollection(ws.ID, sc.ID, "项目", "目录集合", "ask")
	if err != nil {
		t.Fatalf("建集合失败: %v", err)
	}
	if err := d.UpdateCollection(c.ID, map[string]interface{}{"icon": "folder", "description": "主要项目"}); err != nil {
		t.Fatalf("更新集合失败: %v", err)
	}
	it, err := d.CreateItem(ws.ID, c.ID, "API 服务", "目录", "D:/proj/api")
	if err != nil {
		t.Fatalf("建条目失败: %v", err)
	}
	if err := d.UpdateItem(it.ID, map[string]interface{}{
		"working_directory": "D:/proj/api", "tool": "vscode", "args": "--new-window",
		"remark": "主接口层", "color": "#5e6ad2", "sort": 3,
	}); err != nil {
		t.Fatalf("更新条目失败: %v", err)
	}
	// 故意乱序插入，且 sort 与插入顺序不一致：DB 层不带 ORDER BY，若导出端漏了
	// 「按 sort 排序」，往返后的顺序就会退回插入顺序，这里才能抓到。
	for _, spec := range []struct {
		name string
		sort int
	}{{"先插但排最后", 9}, {"后插但排最前", 0}} {
		x, err := d.CreateItem(ws.ID, c.ID, spec.name, "目录", "D:/proj/"+spec.name)
		if err != nil {
			t.Fatalf("建条目 %s 失败: %v", spec.name, err)
		}
		if err := d.UpdateItem(x.ID, map[string]interface{}{"sort": spec.sort}); err != nil {
			t.Fatalf("更新条目 %s 排序失败: %v", spec.name, err)
		}
	}

	out := filepath.Join(t.TempDir(), "scene.json")
	mustOK(t, svc.SceneExport(sc.ID, out), "导出场景")

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读导出文件失败: %v", err)
	}
	var pkg ScenePackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("导出文件不是合法 JSON: %v", err)
	}
	if pkg.Schema != scenePackageSchema || pkg.Version != scenePackageVersion {
		t.Errorf("包标识不符: schema=%q version=%d", pkg.Schema, pkg.Version)
	}
	// 身份字段不得出现在文件里：id 跨机器导入会冲突，统计字段串味。
	if len(pkg.Scene.Env) != 1 || pkg.Scene.Env[0].Runtime != "mysql" || pkg.Scene.Env[0].Version != "8.4.3" {
		t.Errorf("环境绑定未正确导出: %+v", pkg.Scene.Env)
	}

	// 导入到一个全新的工作空间，模拟「同事拿到文件」
	ws2, err := d.CreateWorkspace("同事的空间")
	if err != nil {
		t.Fatalf("建第二个工作空间失败: %v", err)
	}
	data := mustOK(t, svc.SceneImport(ws2.ID, out), "导入场景")
	res, ok := data.(SceneImportResult)
	if !ok {
		t.Fatalf("导入结果类型异常: %T", data)
	}
	if res.SceneName != "后端开发" || res.Collections != 1 || res.Items != 3 {
		t.Fatalf("导入计数不符: %+v", res)
	}
	// Env 为 nil → serviceableRuntimes 为空 → mysql 应被记为跳过而非让导入失败
	if len(res.SkippedEnv) != 1 || res.SkippedEnv[0] != "mysql" {
		t.Errorf("未安装运行时应记入 skippedEnv，实际: %+v", res.SkippedEnv)
	}

	// 逐项核对落库结果
	sc2, err := d.GetScene(res.SceneID)
	if err != nil {
		t.Fatalf("取回导入场景失败: %v", err)
	}
	if sc2.Name != "后端开发" || sc2.Type != "通用" || sc2.Description != "后端日常" ||
		sc2.Icon != "server" || sc2.Color != "#cfa93f" {
		t.Errorf("场景字段未完整还原: %+v", sc2)
	}
	if sc2.ID == sc.ID {
		t.Error("导入必须新建场景，不能复用原 id")
	}

	colls, err := d.ListCollections(res.SceneID)
	if err != nil || len(colls) != 1 {
		t.Fatalf("导入集合数不符: %v len=%d", err, len(colls))
	}
	got := colls[0]
	if got.Name != "项目" || got.Type != "目录集合" || got.OpenStrategy != "ask" ||
		got.Icon != "folder" || got.Description != "主要项目" {
		t.Errorf("集合字段未完整还原: %+v", got)
	}
	if got.WorkspaceID != ws2.ID || got.SceneID != res.SceneID {
		t.Errorf("集合未挂到目标工作空间/新场景: %+v", got)
	}

	items, err := d.ListItems(got.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("导入条目数不符: %v len=%d", err, len(items))
	}
	// 顺序由数组位置承载：导入端按下标重排 sort，故原 sort 0/3/9 应变为 0/1/2。
	// 这同时验证了导出端确实按 sort 排序（否则「先插但排最后」会拿到更小的下标）。
	wantSort := map[string]int{"后插但排最前": 0, "API 服务": 1, "先插但排最后": 2}
	for _, gi := range items {
		want, known := wantSort[gi.Name]
		if !known {
			t.Fatalf("出现未预期的条目: %q", gi.Name)
		}
		if gi.Sort != want {
			t.Errorf("条目 %q 顺序未还原: got sort=%d want=%d", gi.Name, gi.Sort, want)
		}
		if gi.Name == "API 服务" {
			if gi.Type != "目录" || gi.Value != "D:/proj/api" {
				t.Errorf("条目基础字段未还原: %+v", gi)
			}
			if gi.WorkingDirectory != "D:/proj/api" || gi.Tool != "vscode" || gi.Args != "--new-window" ||
				gi.Remark != "主接口层" || gi.Color != "#5e6ad2" {
				t.Errorf("条目附加字段未还原: %+v", gi)
			}
			if gi.ID == it.ID {
				t.Error("条目应重新生成 id")
			}
		}
	}
}

// TestSceneImportNameConflict 同名场景重复导入不得失败，应追加后缀。
// 一个文件导入两次是完全正常的操作（换机器、试错），不该报「名称已存在」。
func TestSceneImportNameConflict(t *testing.T) {
	svc, d := newTestService(t)
	ws, err := d.CreateWorkspace("空间")
	if err != nil {
		t.Fatalf("建工作空间失败: %v", err)
	}
	sc, err := d.CreateScene(ws.ID, "重复场景", "通用")
	if err != nil {
		t.Fatalf("建场景失败: %v", err)
	}
	out := filepath.Join(t.TempDir(), "s.json")
	mustOK(t, svc.SceneExport(sc.ID, out), "导出")

	for _, want := range []string{"重复场景 (导入)", "重复场景 (导入 2)"} {
		data := mustOK(t, svc.SceneImport(ws.ID, out), "导入")
		res, ok := data.(SceneImportResult)
		if !ok {
			t.Fatalf("导入结果类型异常: %T", data)
		}
		if res.SceneName != want {
			t.Errorf("同名去重结果不符: got=%q want=%q", res.SceneName, want)
		}
	}
	// 原有场景必须原封不动
	if _, err := d.GetScene(sc.ID); err != nil {
		t.Errorf("导入不应影响原场景: %v", err)
	}
}

// TestSceneImportRejectsBadInput 坏输入要给出明确错误而不是 panic 或半成功。
func TestSceneImportRejectsBadInput(t *testing.T) {
	svc, d := newTestService(t)
	ws, err := d.CreateWorkspace("空间")
	if err != nil {
		t.Fatalf("建工作空间失败: %v", err)
	}

	dir := t.TempDir()
	notJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(notJSON, []byte("{not json"), 0644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	noName := filepath.Join(dir, "noname.json")
	pkg := ScenePackage{Schema: scenePackageSchema, Version: scenePackageVersion}
	body, _ := json.Marshal(pkg)
	if err := os.WriteFile(noName, body, 0644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}
	tooNew := filepath.Join(dir, "future.json")
	pkg2 := ScenePackage{Schema: scenePackageSchema, Version: scenePackageVersion + 1,
		Scene: SceneSpec{Name: "未来场景"}}
	body2, _ := json.Marshal(pkg2)
	if err := os.WriteFile(tooNew, body2, 0644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	cases := []struct{ name, path, wantSub string }{
		{"非法 JSON", notJSON, "不是有效的场景文件"},
		{"缺少场景名", noName, "缺少场景名称"},
		{"版本过高", tooNew, "高于当前支持"},
		{"文件不存在", filepath.Join(dir, "nope.json"), "读取文件失败"},
		{"空路径", "", "导入路径为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := svc.SceneImport(ws.ID, c.path)
			if r == nil || r.Code == 0 {
				t.Fatalf("期望失败，实际: %+v", r)
			}
			if c.wantSub != "" && !strings.Contains(r.Msg, c.wantSub) {
				t.Errorf("错误信息未包含 %q: %s", c.wantSub, r.Msg)
			}
		})
	}

	if r := svc.SceneImport("", dir); r == nil || r.Code == 0 {
		t.Error("未指定工作空间应失败")
	}
	if r := svc.SceneImport(ws.ID, "not-exist.json"); r == nil || r.Code == 0 {
		t.Error("未知工作空间/不存在文件应失败")
	}
}
