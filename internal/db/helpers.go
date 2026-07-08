package db

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newID() string {
	return uuid.New().String()
}

// camelToSnake 驼峰 → 蛇形：createdAt → created_at, workspaceId → workspace_id
func camelToSnake(s string) string {
	var result strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				result.WriteByte('_')
			}
			result.WriteRune(r + 32) // to lowercase
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// structToMap 将结构体转为 map（供 BulkInsert 使用）
// 使用 reflect 按 json tag 取 key，再转为蛇形 → 匹配数据库列名
func structToMap(v interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return result
	}
	t := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		key := field.Tag.Get("json")
		if key != "" {
			// json tag 是 camelCase，但数据库列是 snake_case，需要转换
			if idx := strings.Index(key, ","); idx > 0 {
				key = key[:idx] // 去掉 ",omitempty" 等选项
			}
			key = camelToSnake(key)
		} else {
			key = camelToSnake(field.Name)
		}
		result[key] = rv.Field(i).Interface()
	}
	return result
}

func mapToWorkspace(m map[string]interface{}) Workspace {
	return Workspace{
		ID:        str(m["id"]),
		Name:      str(m["name"]),
		Storage:   str(m["storage"]),
		Remark:    str(m["remark"]),
		CreatedAt: str(m["created_at"]),
		UpdatedAt: str(m["updated_at"]),
	}
}

func mapToScene(m map[string]interface{}) Scene {
	return Scene{
		ID:          str(m["id"]),
		WorkspaceID: str(m["workspace_id"]),
		Name:        str(m["name"]),
		Type:        str(m["type"]),
		Description: str(m["description"]),
		Icon:        str(m["icon"]),
		Color:       str(m["color"]),
		Favorite:    integer(m["favorite"]),
		Unbound:     integer(m["unbound"]),
		UsageCount:  integer(m["usage_count"]),
		Sort:        integer(m["sort"]),
		CreatedAt:   str(m["created_at"]),
		UpdatedAt:   str(m["updated_at"]),
	}
}

func mapToCollection(m map[string]interface{}) Collection {
	return Collection{
		ID:            str(m["id"]),
		WorkspaceID:   str(m["workspace_id"]),
		SceneID:       str(m["scene_id"]),
		Name:          str(m["name"]),
		Type:          str(m["type"]),
		Description:   str(m["description"]),
		DefaultToolID: str(m["default_tool_id"]),
		Tool:          str(m["tool"]),
		Icon:          str(m["icon"]),
		Color:         str(m["color"]),
		OpenStrategy:  str(m["open_strategy"]),
		Favorite:      integer(m["favorite"]),
		Recent:        integer(m["recent"]),
		RecentAt:      str(m["recent_at"]),
		Unbound:       integer(m["unbound"]),
		PluginID:      str(m["plugin_id"]),
		UsageCount:    integer(m["usage_count"]),
		Sort:          integer(m["sort"]),
		CreatedAt:     str(m["created_at"]),
		UpdatedAt:     str(m["updated_at"]),
	}
}

func mapToItem(m map[string]interface{}) CollectionItem {
	return CollectionItem{
		ID:               str(m["id"]),
		WorkspaceID:      str(m["workspace_id"]),
		CollectionID:     str(m["collection_id"]),
		Name:             str(m["name"]),
		Type:             str(m["type"]),
		Value:            str(m["value"]),
		WorkingDirectory: str(m["working_directory"]),
		ToolID:           str(m["tool_id"]),
		Tool:             str(m["tool"]),
		Args:             str(m["args"]),
		Icon:             str(m["icon"]),
		Color:            str(m["color"]),
		Remark:           str(m["remark"]),
		PluginData:       str(m["plugin_data"]),
		UsageCount:       integer(m["usage_count"]),
		Sort:             integer(m["sort"]),
		CreatedAt:        str(m["created_at"]),
		UpdatedAt:        str(m["updated_at"]),
	}
}

func mapToOpenTool(m map[string]interface{}) OpenTool {
	return OpenTool{
		ID:        str(m["id"]),
		Name:      str(m["name"]),
		Type:      str(m["type"]),
		Path:      str(m["path"]),
		Args:      str(m["args"]),
		IsDefault: integer(m["is_default"]),
	}
}

func mapToSnapshot(m map[string]interface{}) Snapshot {
	return Snapshot{
		ID:        str(m["id"]),
		Kind:      str(m["kind"]),
		Label:     str(m["label"]),
		Note:      str(m["note"]),
		Payload:   str(m["payload"]),
		Size:      int64(integer(m["size"])),
		CreatedAt: str(m["created_at"]),
	}
}

func str(v interface{}) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

func integer(v interface{}) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		var n int
		fmt.Sscanf(val, "%d", &n)
		return n
	}
	return 0
}

func mapSlice[T any](rows []map[string]interface{}, mapper func(map[string]interface{}) T) []T {
	result := make([]T, len(rows))
	for i, row := range rows {
		result[i] = mapper(row)
	}
	return result
}

// nameExists 检查表中是否存在匹配条件的记录
func (d *Database) nameExists(table, where string, params ...interface{}) (bool, error) {
	count, err := d.CountWhere(table, where, params...)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// validateName 校验并清理用户输入的名称：去除首尾空白，检查空值
func validateName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r >= 0x20 && r != 0x7F {
			b.WriteRune(r)
		}
	}
	return b.String()
}
