package sshx

import (
	"quickdock/internal/db"
	"quickdock/services"
)

// SshCommandInput 命令片段表单入参。
//
// 刻意不直接收 db.SshCommand：usageCount / createdAt / updatedAt 是库里的状态，
// 让表单往返携带它们等于给了前端一个改计数和时间的口子。
type SshCommandInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Sort        int    `json:"sort"`
}

// ListSshCommands 列出全部命令片段。data: []db.SshCommand
func (s *SshService) ListSshCommands() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	list, err := s.App.DB.ListSshCommands()
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(list)
}

// SaveSshCommand 新增或更新一条命令片段（id 为空即新增）。data: db.SshCommand
func (s *SshService) SaveSshCommand(in SshCommandInput) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	c, err := s.App.DB.SaveSshCommand(&db.SshCommand{
		ID:          in.ID,
		Name:        in.Name,
		Command:     in.Command,
		Description: in.Description,
		Category:    in.Category,
		Sort:        in.Sort,
	})
	if err != nil {
		return failSsh(err)
	}
	return services.Ok(c)
}

// DeleteSshCommand 删除一条命令片段。data: nil
func (s *SshService) DeleteSshCommand(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := s.App.DB.DeleteSshCommand(id); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// TouchSshCommand 片段被执行一次后累加使用次数。data: nil
//
// 前端在执行成功后调用；调用失败不该影响命令本身的执行结果，所以这里
// 的返回值前端可以忽略。
func (s *SshService) TouchSshCommand(id string) *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	if err := s.App.DB.TouchSshCommand(id); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}
