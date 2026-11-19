package db

// 白名单守卫：新增字段忘了登记 `validColumns` 时，这里会立刻红。
//
// 同一个坑踩过两次：
//   - 本会话开头：`AISpace.TaskGone` → 「非法列名: task_gone」；
//   - 加 `AIMessage.ReasoningRounds` → 「非法列名: reasoning_rounds」，
//     一次性红掉 4 个测试，而**线上表现更糟**：任何一条 AI 消息都写不进库。
//
// 为什么它防不住：`structToMap` 只跳过 `db:"-"`，其余字段一律转列名去 INSERT；
// 而列名是否合法由一张**全局**白名单决定（不按表分）。所以「加一个字段」这个
// 最普通的动作，会静默地让整条写入失败——编译通过、类型检查通过。
//
// 这个测试把「结构体字段」与「白名单」放在一起对账：漏一个就点名指出来。

import (
	"reflect"
	"testing"
)

// insertSamples 所有走 structToMap + BulkInsert 的模型。
//
// 新增「会被整条插入」的结构体时把它加进来：这个列表本身就是那份契约。
func insertSamples() []interface{} {
	return []interface{}{
		&AIMessage{},
		&AIConversation{},
		&AISpace{},
		&AgentRun{},
		&AgentNode{},
		&AgentEvent{},
		&CollectionItem{},
		&Collection{},
		&Workspace{},
		&Scene{},
		&OpenTool{},
		&Snapshot{},
	}
}

// 结构体里每个「会落库」的字段，都必须在 validColumns 里登记。
func TestModelFieldsAreRegisteredColumns(t *testing.T) {
	for _, sample := range insertSamples() {
		cols := structToMap(sample)
		if len(cols) == 0 {
			t.Errorf("%T 没解析出任何列，样本写错了？", sample)
			continue
		}
		for col := range cols {
			if err := validateColumn(col); err != nil {
				t.Errorf("%T 的字段 %q 没进白名单：请在 internal/db/db.go 的 validColumns 里补 \"%s\": true",
					sample, col, col)
			}
		}
	}
}

// 结构体里带 `db:"-"` 的字段绝不能出现在 map 里——那是「只读/计算字段」的
// 唯一标记，漏了它就等于把不存在的列塞进 INSERT。
func TestDbDashFieldsAreExcluded(t *testing.T) {
	cols := structToMap(&AISpace{})
	typed := reflect.TypeOf(AISpace{})
	for i := 0; i < typed.NumField(); i++ {
		f := typed.Field(i)
		if f.Tag.Get("db") != "-" {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" {
			continue
		}
		name := tag
		if idx := len(tag); idx > 0 {
			// json:"task_gone" → task_gone（去掉 ,omitempty 之类）
			for j := 0; j < len(tag); j++ {
				if tag[j] == ',' {
					name = tag[:j]
					break
				}
			}
		}
		if _, ok := cols[name]; ok {
			t.Errorf("db:\"-\" 的字段 %s 仍然出现在待插入列里", f.Name)
		}
	}
}

// 插进表里的每一列，都必须真的存在于那张表。
//
// 为什么单靠上面的白名单检查不够：`validColumns` 是**全局**集合、不按表分，
// 只要某个通用词（kind / count / title / content / note …）在别处登记过，
// 新表加同名字段就能通过白名单，但运行时该表**所有** INSERT 都会因为
// `no such column` 整条回滚——表现与 reasoning_rounds 那次一模一样，
// 而上面的守卫全绿。所以这里补一条「结构体字段 ⊆ 目标表实际列」。
func TestModelColumnsExistInTheirTables(t *testing.T) {
	d := openTestDB(t)
	pairs := []struct {
		table  string
		sample interface{}
	}{
		{"ai_messages", &AIMessage{}},
		{"ai_conversations", &AIConversation{}},
		{"ai_spaces", &AISpace{}},
		{"agent_runs", &AgentRun{}},
		{"agent_nodes", &AgentNode{}},
		{"agent_events", &AgentEvent{}},
		{"items", &CollectionItem{}},
		{"collections", &Collection{}},
		{"workspaces", &Workspace{}},
		{"scenes", &Scene{}},
		{"tools", &OpenTool{}},
		{"snapshots", &Snapshot{}},
	}
	for _, p := range pairs {
		for col := range structToMap(p.sample) {
			if !d.hasColumn(p.table, col) {
				t.Errorf("%s 的字段 %q 在表 %s 里不存在列：该表的所有 INSERT 都会失败（no such column）",
					reflectTypeName(p.sample), col, p.table)
			}
		}
	}
}

// reflectTypeName 只用于报错信息，避免打出一长串指针地址。
func reflectTypeName(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).Elem().Name()
}
