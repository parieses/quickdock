package ai

// 编辑快照与回滚（方案 A3 的宿主侧，存储层见 internal/db/ai_checkpoint.go）。
//
// 为什么需要它：diff（A2）只解决「看得见改了什么」，改错了仍然只能靠 git。
// 而 QuickDock 面向的场景里有一半根本没有 git——改一份配置、修个脚本、
// 动一个不是仓库的文件。那时「撤不回」等于「不敢让 AI 碰文件」。
//
// 与别家不同的三个取舍：
//  1. 只快照「这次改到的文件」，不做整仓。Cursor 做整仓是因为它有「工作区」概念，
//     而这里一次对话可能东改一个西改两个，整仓快照的成本与收益完全不成比例。
//  2. 快照文件落在数据目录，不进库（理由见 db 层注释）。
//  3. 回滚本身可再撤销：恢复前先把当前状态打一个 undo 快照。少了这一步，
//     用户点错一次就真没了——而「点错」在时间轴上一秒就能发生。
//
// 目录布局：<dataDir>/ai_checkpoints/<convID>/<checkpointID>/<base64url(绝对路径)>
//
// 用 checkpointID 而不是 seq 做目录名：seq 由 DB 的 MAX+1 分配，若先建目录再插行，
// 就得把「分配编号」拆成两步并自己处理撞车；而 id 在复制文件之前就已经存在了。
// seq 只用于展示与排序，不参与文件定位。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

const (
	// aiCheckpointMaxBytes 单个文件的快照上限。
	//
	// 超过就不许编辑：二进制与大文件本来就不该走 file_edit（那要把它整个读进内存
	// 再整份写回）。明确拒绝比「快照了一个半截文件」好得多——后者会在回滚时
	// 把原文件写坏，而那时用户以为自己在恢复。
	aiCheckpointMaxBytes = 5 << 20
)

// checkpointRoot 快照根目录。
//
// 每次现算而不是缓存在字段里：数据目录可以在设置里改（SetDataDir），缓存下来
// 就会在改完之后继续往旧目录写，而设置页显示的是「已迁移完成」。
func checkpointRoot() string {
	return filepath.Join(platform.DefaultDataDir(), "ai_checkpoints")
}

// checkpointStore 一次会话的快照写入器，实现 aitools.CheckpointHook。
type checkpointStore struct {
	db     *db.Database
	convID string
}

func newCheckpointStore(database *db.Database, convID string) *checkpointStore {
	if database == nil || strings.TrimSpace(convID) == "" {
		return nil
	}
	return &checkpointStore{db: database, convID: convID}
}

// Snapshot 把 path 的当前状态记进这次会话的回滚目录。
//
// 失败必须返回错误、让工具放弃写入：快照没成功还照写，等于「改了却撤不回」，
// 比不改更糟——界面上的回滚按钮会让人以为还有救。
func (s *checkpointStore) Snapshot(toolCallID, tool, path string) error {
	if s == nil || s.db == nil || strings.TrimSpace(s.convID) == "" {
		return nil // 没有会话上下文：外部 MCP 调用与单测走这条，静默跳过
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("解析路径失败：%v", err)
	}

	file := db.AICheckpointFile{Path: abs}
	fi, statErr := os.Stat(abs)
	switch {
	case statErr == nil:
		if fi.IsDir() {
			return fmt.Errorf("%s 是目录，不能作为编辑快照", abs)
		}
		if fi.Size() > aiCheckpointMaxBytes {
			return fmt.Errorf("%s 有 %d 字节，超过 %dMB 的快照上限，已拒绝编辑（这类文件请用命令处理）",
				filepath.Base(abs), fi.Size(), aiCheckpointMaxBytes>>20)
		}
		file.Existed = true
		file.Mode = uint32(fi.Mode().Perm())
		file.Bytes = fi.Size()
	case os.IsNotExist(statErr):
		// 新建文件：只记 existed=false，回滚时据此删除。不记这一点的后果是
		// 回滚后给用户留下一个空文件，而那个文件本来不该存在。
		file.Existed = false
	default:
		return fmt.Errorf("无法读取 %s：%v", abs, statErr)
	}

	id := uuid.New().String()
	dir := filepath.Join(checkpointRoot(), s.convID, id)
	if file.Existed {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建快照目录失败：%v", err)
		}
		file.Backup = base64.RawURLEncoding.EncodeToString([]byte(abs))
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return fmt.Errorf("读取原文件失败：%v", rerr)
		}
		if werr := os.WriteFile(filepath.Join(dir, file.Backup), data, 0o600); werr != nil {
			return fmt.Errorf("写快照失败：%v", werr)
		}
	}

	filesJSON, _ := json.Marshal([]db.AICheckpointFile{file})
	if _, err := s.db.CreateAICheckpoint(db.AICheckpoint{
		ID:         id,
		ConvID:     s.convID,
		ToolCallID: toolCallID,
		Tool:       tool,
		Files:      string(filesJSON),
		CreatedAt:  time.Now().Format(time.RFC3339),
	}); err != nil {
		// 行写不进去就把已经落下的目录擦掉，避免「磁盘有文件、库里没记录」的孤儿。
		_ = os.RemoveAll(dir)
		return fmt.Errorf("记录回滚点失败：%v", err)
	}
	return nil
}

// AICheckpointView 给前端展示的回滚点（比 db 结构多解析好的文件列表）。
type AICheckpointView struct {
	ID   string `json:"id"`
	Seq  int    `json:"seq"`
	Tool string `json:"tool"`
	// ToolCallID 对应哪一条工具调用：前端据此在卡片上挂「回滚到此处」，
	// 不必让用户去时间轴上找第几步。
	ToolCallID string                `json:"tool_call_id"`
	Files      []db.AICheckpointFile `json:"files"`
	CreatedAt  string                `json:"created_at"`
}

// AIListCheckpoints 列出会话的回滚点（最近的在前）。
func (a *AIService) AIListCheckpoints(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}
	list, err := a.App.DB.ListAICheckpoints(convID)
	if err != nil {
		return services.Fail(err)
	}
	// 空切片而不是 nil：Wails 序列化 nil 会变成 null，前端 forEach 直接抛错，
	// 而「这个会话还没改过文件」是最常见的第一次调用。
	out := make([]AICheckpointView, 0, len(list))
	for _, c := range list {
		v := AICheckpointView{ID: c.ID, Seq: c.Seq, Tool: c.Tool, ToolCallID: c.ToolCallID,
			CreatedAt: c.CreatedAt, Files: []db.AICheckpointFile{}}
		if c.Files != "" {
			_ = json.Unmarshal([]byte(c.Files), &v.Files)
		}
		out = append(out, v)
	}
	return services.Ok(out)
}

// AIRestoreCheckpoint 把文件恢复到某个回滚点之前的状态。
//
// 顺序不能反：
//  1. 先给**当前**磁盘状态打一个 undo 快照（seq 会往后排一个），
//  2. 再按记录覆盖 / 删除。
//
// 少了第 1 步，用户点错一次就真没了。而回滚比编辑更容易点错——它是一排
// 长得一样的圆点里点一个。
//
// 只还原文件，不动消息：与 Cursor 一致。消息是对话历史，删掉会让「刚才那几步」
// 从界面上消失，而用户此刻正需要看着它们判断回滚到哪一步才对。
func (a *AIService) AIRestoreCheckpoint(convID string, seq int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}
	cp, err := a.App.DB.GetAICheckpoint(convID, seq)
	if err != nil {
		return services.Fail(err)
	}
	if cp == nil {
		return services.FailMsg("回滚点不存在（可能已被清理），请刷新列表")
	}
	var files []db.AICheckpointFile
	if cp.Files != "" {
		if uerr := json.Unmarshal([]byte(cp.Files), &files); uerr != nil {
			return services.FailMsg("回滚点数据损坏，无法还原：" + uerr.Error())
		}
	}
	if len(files) == 0 {
		return services.FailMsg("这条回滚点没有记录任何文件")
	}

	// 1) 打 undo 快照。这里复用同一个 store，tool 记 "restore" 便于界面上区分
	//    「这一次是回滚动作自己产生的点」。
	undo := newCheckpointStore(a.App.DB, convID)
	for _, f := range files {
		if perr := undo.Snapshot("", "restore", f.Path); perr != nil {
			return services.FailMsg("无法为回滚建立安全点，已取消：" + perr.Error())
		}
	}

	// 2) 还原。逐个文件处理，出错即停并如实报告已经改了哪几个——
	//    「部分成功」必须说清，否则用户不知道现在磁盘处在什么状态。
	dir := filepath.Join(checkpointRoot(), cp.ConvID, cp.ID)
	var done []string
	for _, f := range files {
		if cerr := restoreOneFile(dir, f); cerr != nil {
			return services.FailMsg(fmt.Sprintf("已还原 %d/%d 个文件后失败：%v（撤销点已建立，可回滚本次回滚）",
				len(done), len(files), cerr))
		}
		done = append(done, f.Path)
	}
	logger.I("[ai-checkpoint] 回滚 conv=%s seq=%d 还原 %d 个文件", convID, seq, len(done))
	return services.Ok(map[string]any{
		"restored": len(done),
		"files":    done,
		"note":     "已还原文件内容（消息未改动）。本次回滚本身也可撤销：列表里最新那条是回滚前状态。",
	})
}

// restoreOneFile 还原一个文件。
func restoreOneFile(dir string, f db.AICheckpointFile) error {
	if !f.Existed {
		// 改动前不存在 → 回滚 = 删掉它。不存在也不算错（用户可能已经自己删了）。
		if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除新建文件 %s 失败：%v", f.Path, err)
		}
		return nil
	}
	if f.Backup == "" {
		return fmt.Errorf("%s 缺少快照文件名，无法还原", f.Path)
	}
	data, err := os.ReadFile(filepath.Join(dir, f.Backup))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s 的快照文件已不存在（可能被清理），无法还原", filepath.Base(f.Path))
		}
		return fmt.Errorf("读取快照失败：%v", err)
	}
	mode := os.FileMode(f.Mode)
	if mode == 0 {
		mode = 0o644
	}
	// 目录可能已随文件一起被删掉（比如模型 file_write 新建后用户删了整个目录）。
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%v", err)
	}
	// 先写临时文件再改名：回滚写到一半失败会留下半截文件，那比不回滚更糟。
	tmp := f.Path + ".qd-restore"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("写入失败：%v", err)
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换文件失败：%v", err)
	}
	return nil
}

// purgeAICheckpoints 启动时清理过老与超量的回滚点，连带删掉它们的快照目录。
//
// 与剪贴板过期、墓碑过期同一趟做（见 lifecycle.go）：三者都是「启动时扫一遍、
// 平时不打扰」。不能改成「删会话时顺便清」——一个不再删东西的老库正好是最需要清的。
func purgeAICheckpoints(database *db.Database) {
	if database == nil {
		return
	}
	doomed, err := database.PurgeAICheckpoints(db.AICheckpointMaxAgeDays, db.AICheckpointKeepPerConv)
	if err != nil {
		logger.W("[ai-checkpoint] 清理回滚点失败: %v", err)
		return
	}
	root := checkpointRoot()
	for _, c := range doomed {
		// 只删这一条自己的目录：同一个会话的其它回滚点还在用同一个父目录。
		_ = os.RemoveAll(filepath.Join(root, c.ConvID, c.ID))
	}
	if len(doomed) > 0 {
		logger.I("[ai-checkpoint] 已清理 %d 个过期回滚点", len(doomed))
	}
}
