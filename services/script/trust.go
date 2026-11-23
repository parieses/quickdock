package script

import (
	"errors"
	"os"
	"path/filepath"

	"quickdock/services"
)

// 这两个错误要让用户一眼分辨「该点确认」还是「该等一等」，
// 所以各自有个明确的文案而不是共用一句「运行失败」。
var (
	// errUntrustedScript 外部导入的脚本还没被确认过。
	errUntrustedScript = errors.New("这个脚本来自外部导入，尚未确认。请点「确认运行」")
	// errAlreadyRunning 撞上并发上限。
	errAlreadyRunning = errors.New("该脚本正在运行中，请等它结束或先停止")
)

// trustMarker 可信标记文件名（位于脚本目录内）。
//
// 用目录里的空文件而不是 scripts 表加一列：加列要动 schema/迁移/列白名单/
// CRUD 六七处，漏一处就静默丢数据；而「目录里有没有这个文件」零成本地跟随
// 目录一起被复制、被导出 zip、被放进回收区，语义正好。
const trustMarker = ".qd-trusted"

// scriptTrusted 该脚本是否已被用户确认可以运行。
func (s *ScriptService) scriptTrusted(id string) bool {
	_, err := os.Stat(filepath.Join(s.dir(id), trustMarker))
	return err == nil
}

// markTrusted 写入可信标记。
func (s *ScriptService) markTrusted(id string) error {
	return os.WriteFile(filepath.Join(s.dir(id), trustMarker), []byte("1\n"), 0o644)
}

// TrustScript 用户确认「这个外部脚本我愿意跑」。
//
// 导入的 zip / 别人发的脚本目录里可能是一行 `rm -rf ~`。跑脚本等于以当前
// 用户身份执行任意代码，所以第一次手动运行前必须有一次显式确认；确认过就
// 记住，不用每次都问。
func (s *ScriptService) TrustScript(id string) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	if _, err := s.App.DB.GetScript(id); err != nil {
		return services.Fail(err)
	}
	if err := s.markTrusted(id); err != nil {
		return services.Fail(err)
	}
	s.emit(EventChanged, map[string]any{"scriptId": id, "reason": "trust"})
	return services.Ok(nil)
}
