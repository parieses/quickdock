package services

import "log"

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

func Fail(err error) *ApiResult {
	if err == nil {
		return &ApiResult{Code: 1, Msg: "unknown error"}
	}
	log.Printf("[ERR] %v", err)
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

// 确保 DB 初始化（批量前置检查）
// 注意：不再需要手动加锁，db.Database 内部已加锁
func (a *AppService) dbOK() *ApiResult {
	if a.DB == nil {
		log.Println("[ERR] database not initialized")
		return FailMsg("database not initialized")
	}
	return nil
}
