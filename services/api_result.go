package services

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// ApiResult 统一 API 返回结构
// code: 0=成功, 1=失败
type ApiResult struct {
	Code int         `json:"code"`
	Data interface{} `json:"data"`
	Msg  string      `json:"msg"`
}

func Ok(data interface{}) *ApiResult {
	return &ApiResult{Code: 0, Data: data, Msg: "ok"}
}

func OkMsg(data interface{}, msg string) *ApiResult {
	return &ApiResult{Code: 0, Data: data, Msg: msg}
}

// callerSite 返回首个「业务代码」调用帧（形如 service.go:123），用于给统一错误日志标注来源。
//
// Fail/Wrap 是全后端共用出口，若只记 err 文本，日志里出现 "[ERR] no such table: xxx"
// 时无法判断是哪个接口触发的。此处向上跳栈，跳过本文件的包装帧，取到真正的调用点。
// 仅在错误路径调用，runtime.Caller 的开销可忽略。
func callerSite() string {
	for i := 2; i < 8; i++ {
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		if strings.HasSuffix(file, "api_result.go") {
			continue // wrap/Wrap 等包装帧，继续向上找业务调用点
		}
		return fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	return "unknown"
}

func Fail(err error) *ApiResult {
	if err == nil {
		return &ApiResult{Code: 1, Msg: "unknown error"}
	}
	logger.E("[ERR] %s: %v", callerSite(), err)
	return &ApiResult{Code: 1, Msg: err.Error()}
}

func FailMsg(msg string) *ApiResult {
	return &ApiResult{Code: 1, Msg: msg}
}

// GetValue 读取键值
func (a *AppService) GetValue(key string) *ApiResult {
	if a.DB == nil {
		return FailMsg("database not initialized")
	}
	val, err := a.DB.GetValue(key)
	if err != nil {
		return Fail(err)
	}
	return Ok(val)
}

// SetValue 写入键值
func (a *AppService) SetValue(key, value string) *ApiResult {
	if a.DB == nil {
		return FailMsg("database not initialized")
	}
	if err := a.DB.SetValue(key, value); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// wrap 泛型包装：(值, 错误) → *ApiResult。
// 用于消除 service 层 "val, err := ...; if err != nil { return Fail(err) }; return Ok(val)" 的重复模板。
func wrap[T any](val T, err error) *ApiResult {
	if err != nil {
		return Fail(err)
	}
	return Ok(val)
}

// Wrap 是 wrap 的导出版本，供 services 包以外的子包（如 services/plugin）调用。
func Wrap[T any](val T, err error) *ApiResult {
	return wrap(val, err)
}

// 确保 DB 初始化（批量前置检查）
// 注意：不再需要手动加锁，db.Database 内部已加锁
func (a *AppService) dbOK() *ApiResult {
	return CheckDB(a.DB)
}

// CheckDB 校验数据库是否已完成注入（Wails 在 ServiceStartup 阶段注入），未注入返回失败结果。
// 各 service 的 dbOK() 统一委托至此：DB 未就绪时前端会拿到明确错误而非 nil 解引用崩溃。
func CheckDB(d *db.Database) *ApiResult {
	if d == nil {
		logger.E("QuickDock: database not initialized (%s)", callerSite())
		return FailMsg("database not initialized")
	}
	return nil
}
