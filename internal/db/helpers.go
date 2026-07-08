package db

import (
	"fmt"
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

// structToMap 将结构体转为 map（供 BulkInsert 使用）
func structToMap(v interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	switch val := v.(type) {
	case *Workspace:
		result["id"] = val.ID
		result["name"] = val.Name
		result["storage"] = val.Storage
		result["remark"] = val.Remark
		result["created_at"] = val.CreatedAt
		result["updated_at"] = val.UpdatedAt
	case *Scene:
		result["id"] = val.ID
		result["workspace_id"] = val.WorkspaceID
		result["name"] = val.Name
		result["type"] = val.Type
		result["description"] = val.Description
		result["icon"] = val.Icon
		result["color"] = val.Color
		result["favorite"] = val.Favorite
		result["unbound"] = val.Unbound
		result["usage_count"] = val.UsageCount
		result["sort"] = val.Sort
		result["created_at"] = val.CreatedAt
		result["updated_at"] = val.UpdatedAt
	case *Collection:
		result["id"] = val.ID
		result["workspace_id"] = val.WorkspaceID
		result["scene_id"] = val.SceneID
		result["name"] = val.Name
		result["type"] = val.Type
		result["description"] = val.Description
		result["default_tool_id"] = val.DefaultToolID
		result["tool"] = val.Tool
		result["icon"] = val.Icon
		result["color"] = val.Color
		result["open_strategy"] = val.OpenStrategy
		result["favorite"] = val.Favorite
		result["recent"] = val.Recent
		result["recent_at"] = val.RecentAt
		result["unbound"] = val.Unbound
		result["plugin_id"] = val.PluginID
		result["usage_count"] = val.UsageCount
		result["sort"] = val.Sort
		result["created_at"] = val.CreatedAt
		result["updated_at"] = val.UpdatedAt
	case *CollectionItem:
		result["id"] = val.ID
		result["workspace_id"] = val.WorkspaceID
		result["collection_id"] = val.CollectionID
		result["name"] = val.Name
		result["type"] = val.Type
		result["value"] = val.Value
		result["working_directory"] = val.WorkingDirectory
		result["tool_id"] = val.ToolID
		result["tool"] = val.Tool
		result["args"] = val.Args
		result["icon"] = val.Icon
		result["color"] = val.Color
		result["remark"] = val.Remark
		result["plugin_data"] = val.PluginData
		result["usage_count"] = val.UsageCount
		result["sort"] = val.Sort
		result["created_at"] = val.CreatedAt
		result["updated_at"] = val.UpdatedAt
	case *OpenTool:
		result["id"] = val.ID
		result["name"] = val.Name
		result["type"] = val.Type
		result["path"] = val.Path
		result["args"] = val.Args
		result["is_default"] = val.IsDefault
	case *Snapshot:
		result["id"] = val.ID
		result["kind"] = val.Kind
		result["label"] = val.Label
		result["note"] = val.Note
		result["payload"] = val.Payload
		result["size"] = val.Size
		result["created_at"] = val.CreatedAt
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
