// Package fsutil 提供不带业务语义、只依赖标准库的文件读写小工具。
// 刻意保持叶子包：不 import internal 下任何其它包，避免被 env / plugin / services 反向依赖成环。
package fsutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ReadJSON 读文件并反序列化为 T。读失败与解析失败都返回 error，错误统一带路径并用 %w 包装，
// 调用方可用 errors.Is(err, fs.ErrNotExist) 区分「文件不存在」。
//
// 不内置 logging / 删文件等副作用：这些属于调用点的业务决策。
func ReadJSON[T any](path string) (T, error) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return v, nil
}

// TryReadJSON 是「宽容加载」版本：文件不存在、读失败或解析失败都返回零值 + false，
// 覆盖各 loadXxx() 里「读不到就用默认配置」的共享口径。
//
// 需要区分不存在 / 解析失败并做不同处理时，用 ReadJSON 自行判断，不要用本函数。
func TryReadJSON[T any](path string) (T, bool) {
	var v T
	data, err := os.ReadFile(path)
	if err != nil {
		return v, false
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, false
	}
	return v, true
}

// IsNotExist 便于调用方在 ReadJSON 的错误上判断文件不存在（错误用 %w 包装过）。
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
