package ai

import (
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 任务产物目录：AI 生成的文件往哪儿放。
//
// 两条规则（2026-10-30 定）：
//   - 属于空间的任务：直接用空间目录。产物落在项目里，AI 能顺手改项目已有的文件，
//     这也是「空间 = 一个目录」的本意。
//   - 不属于空间的任务：在数据目录下给它单独开一个目录。没有空间又没目录时，
//     写操作会被白名单直接拒绝，模型只能干瞪眼——每个任务自带一个目录就没有这种死角。
//
// 目录名用任务 id 的前 8 位而不是标题：标题会变（首条消息后自动生成、手动改名、
// 重新生成），目录名跟着变的话「上一轮刚写进去的文件」就换了个地方，
// AI 和用户都会找不着，而已经发给模型的绝对路径也会失效。
const aiTaskDirName = "ai_tasks"

// ensureTaskDir 给「既没有自己目录、也不属于任何空间」的任务分配并创建专属目录。
//
// 只在确实需要时调用（调用方已确认 conv.Cwd 与空间目录都为空）：不建多余目录，
// 磁盘上留一堆空文件夹同样是负担。创建失败的后果只是「这个任务仍然没有目录」，
// 所以只记日志不返回 error——不能因为建不了目录就让整条对话发不出去。
func (a *AIService) ensureTaskDir(convID string) string {
	convID = strings.TrimSpace(convID)
	if convID == "" || a.App.DB == nil {
		return ""
	}
	short := convID
	if len(short) > 8 {
		short = short[:8]
	}
	dir := filepath.Join(platform.DefaultDataDir(), aiTaskDirName, short)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.W("[ai] 创建任务目录失败 conv=%s dir=%s: %v", convID, dir, err)
		return ""
	}
	// 落库：下一轮不必重建，切走再回来还是同一个目录。
	if err := a.App.DB.SetAIConversationCwd(convID, dir); err != nil {
		logger.W("[ai] 保存任务目录失败 conv=%s: %v", convID, err)
	}
	return dir
}
