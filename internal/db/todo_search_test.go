package db

import "testing"

// TestSearchTodos 命令面板检索待办的过滤口径：
// 命中标题/备注/标签，排除已完成与子任务，并对 LIKE 通配符做字面处理。
func TestSearchTodos(t *testing.T) {
	d := openTestDB(t)

	mk := func(title, note, tags string) *Todo {
		td, err := d.CreateTodo(title, "none", "", note, "", "", "", "", tags)
		if err != nil {
			t.Fatalf("建待办 %q 失败: %v", title, err)
		}
		return td
	}

	weekly := mk("写周报", "本周五前交", `["工作"]`)
	mk("买牛奶", "", "")                   // 标题不命中
	mk("周报模板整理", "模板 path", `["工作"]`)   // 标题命中
	noteOnly := mk("报销", "粘贴到周报表里", "") // 仅备注命中
	sub, err := d.CreateSubtask(weekly.ID, "写周报子项")
	if err != nil {
		t.Fatalf("建子任务失败: %v", err)
	}
	_ = sub
	done := mk("已完成的周报", "", "")
	if err := d.SetTodoStatus(done.ID, "done"); err != nil {
		t.Fatalf("置为完成失败: %v", err)
	}

	got, err := d.SearchTodos("周报", 20)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	hit := map[string]bool{}
	for _, td := range got {
		hit[td.ID] = true
	}
	if !hit[weekly.ID] {
		t.Errorf("标题命中项缺失")
	}
	if !hit[noteOnly.ID] {
		t.Errorf("备注命中项缺失")
	}
	if hit[done.ID] {
		t.Errorf("已完成的待办不该出现在面板检索里")
	}
	subs, err := d.ListTodos()
	if err != nil {
		t.Fatalf("列待办失败: %v", err)
	}
	for _, td := range subs {
		if td.ParentID == weekly.ID && hit[td.ID] {
			t.Errorf("子任务不该出现在面板检索里：%s", td.Title)
		}
	}

	// 标签检索：tags 列存的是 JSON 文本，"工作" 作为关键词也应命中
	byTag, err := d.SearchTodos("工作", 20)
	if err != nil {
		t.Fatalf("按标签检索失败: %v", err)
	}
	if len(byTag) == 0 {
		t.Errorf("标签检索应命中带「工作」标签的待办")
	}

	// 字面通配符：搜 "100%_" 不该退化成「任意字符」而把无关条目全捞回来
	mk("字面百分号", "", "")
	literal, err := d.SearchTodos("100%_", 20)
	if err != nil {
		t.Fatalf("字面检索失败: %v", err)
	}
	if len(literal) != 0 {
		t.Errorf("LIKE 通配符应被转义，实际命中 %d 条", len(literal))
	}

	if empty, err := d.SearchTodos("", 20); err != nil || len(empty) != 0 {
		t.Errorf("空查询必须返回空结果（否则面板每次清空输入都会拉全表）: %d, %v", len(empty), err)
	}
}
