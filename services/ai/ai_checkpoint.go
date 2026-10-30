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

	// aiCheckpointDiskBudget 快照目录的总盘占用上限。
	//
	// 200MB 是按「一次正常改文件的量级」定的：单文件上限 5MB、每会话留 20 个，
	// 一个会话最多 100MB，两个活跃会话就会踩线。超了按最旧优先淘汰——
	// 回滚点的价值随时间衰减，昨天的旧快照不如今天的新快照。
	aiCheckpointDiskBudget int64 = 200 << 20
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

// describe 核算一个文件在快照里的记录项（存在性、权限、大小上限），不落盘。
//
// 单独抽出来给 Snapshot 与 SnapshotMany 共用：两条路对「什么文件能快照」
// 必须有完全一致的判断，否则单文件编辑能改、回滚却建不了安全点。
func (s *checkpointStore) describe(abs string) (db.AICheckpointFile, error) {
	file := db.AICheckpointFile{Path: abs}
	fi, statErr := os.Stat(abs)
	switch {
	case statErr == nil:
		if fi.IsDir() {
			return file, fmt.Errorf("%s 是目录，不能作为编辑快照", abs)
		}
		if fi.Size() > aiCheckpointMaxBytes {
			return file, fmt.Errorf("%s 有 %d 字节，超过 %dMB 的快照上限，已拒绝编辑（这类文件请用命令处理）",
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
		return file, fmt.Errorf("无法读取 %s：%v", abs, statErr)
	}
	return file, nil
}

// copyInto 把原文件内容复制进快照目录，并填好 Backup 文件名。
func copyInto(dir string, file *db.AICheckpointFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建快照目录失败：%v", err)
	}
	file.Backup = base64.RawURLEncoding.EncodeToString([]byte(file.Path))
	data, rerr := os.ReadFile(file.Path)
	if rerr != nil {
		return fmt.Errorf("读取原文件失败：%v", rerr)
	}
	if werr := os.WriteFile(filepath.Join(dir, file.Backup), data, 0o600); werr != nil {
		return fmt.Errorf("写快照失败：%v", werr)
	}
	return nil
}

// Snapshot 把 path 的当前状态记进这次会话的回滚目录。
//
// 失败必须返回错误、让工具放弃写入：快照没成功还照写，等于「改了却撤不回」，
// 比不改更糟——界面上的回滚按钮会让人以为还有救。
func (s *checkpointStore) Snapshot(toolCallID, tool, path string) error {
	return s.SnapshotMany(toolCallID, tool, []string{path})
}

// SnapshotMany 一次为多个文件建**一个**回滚点。
//
// 多文件是为了「回滚本身也能撤销」：一次回滚可能撤掉好几步、涉及好几个文件，
// 若每个文件各建一个点，想撤销这次回滚要点好几次，中途还会被那些点半路拦住。
// 一个点 = 一次动作，才和界面上「点一次」对得上。
func (s *checkpointStore) SnapshotMany(toolCallID, tool string, paths []string) error {
	if s == nil || s.db == nil || strings.TrimSpace(s.convID) == "" {
		return nil // 没有会话上下文：外部 MCP 调用与单测走这条，静默跳过
	}
	seen := make(map[string]bool, len(paths))
	files := make([]db.AICheckpointFile, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("解析路径失败：%v", err)
		}
		if seen[abs] {
			continue // 同一文件被改过多次：当前状态记一份就够
		}
		seen[abs] = true
		file, derr := s.describe(abs)
		if derr != nil {
			return derr
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil
	}

	id := uuid.New().String()
	dir := filepath.Join(checkpointRoot(), s.convID, id)
	for i := range files {
		if !files[i].Existed {
			continue
		}
		if cerr := copyInto(dir, &files[i]); cerr != nil {
			_ = os.RemoveAll(dir)
			return cerr
		}
	}

	filesJSON, _ := json.Marshal(files)
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

// AIRestoreCheckpoint 把文件回退到某个回滚点**之前**的状态。
//
// 「之前」而不是「那一步」：一次提问往往连着改好几步，用户在第 5 步发现方向错了，
// 想回到第 2 步之前——只撤销第 2 步、留着 3/4/5 的改动，磁盘会停在一个谁也没见过
// 的中间态（第 2 步的旧内容 + 后面几步的新内容），而那个状态可能根本编译不过。
//
// 顺序不能反：
//  1. 先给**当前**磁盘状态打一个 undo 快照（seq 会往后排一个），
//  2. 再按 seq 从大到小逐步覆盖 / 删除。
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

	// 目标集合 = 这一步以及它之后的所有步骤（List 是 seq 倒序，正好是还原顺序）。
	all, err := a.App.DB.ListAICheckpoints(convID)
	if err != nil {
		return services.Fail(err)
	}
	var (
		targets []db.AICheckpoint
		paths   []string
	)
	for _, c := range all {
		if c.Seq < seq {
			continue
		}
		var files []db.AICheckpointFile
		if c.Files != "" {
			if uerr := json.Unmarshal([]byte(c.Files), &files); uerr != nil {
				return services.FailMsg(fmt.Sprintf("第 %d 步的回滚点数据损坏，已取消：%v", c.Seq, uerr))
			}
		}
		targets = append(targets, c)
		for _, f := range files {
			paths = append(paths, f.Path)
		}
	}
	if len(targets) == 0 {
		return services.FailMsg("回滚点不存在（可能已被清理），请刷新列表")
	}

	// 1) 打 undo 快照。整次回滚只建一个点（tool 记 "restore"，界面上可区分
	//    「这一次是回滚动作自己产生的点」）——否则撤销这次回滚要点好几次。
	undo := newCheckpointStore(a.App.DB, convID)
	if perr := undo.SnapshotMany("", "restore", paths); perr != nil {
		return services.FailMsg("无法为回滚建立安全点，已取消：" + perr.Error())
	}

	// 2) 还原。按 seq 从大到小：同一文件被改过多次时，最后应用的是最早那一步的
	//    旧内容，结果正好等于「那一步之前」。出错即停并如实报告已经改了哪些——
	//    「部分成功」必须说清，否则用户不知道现在磁盘处在什么状态。
	var (
		done  []string
		seen  = map[string]bool{}
		steps int
	)
	for _, c := range targets {
		var files []db.AICheckpointFile
		if c.Files != "" {
			_ = json.Unmarshal([]byte(c.Files), &files)
		}
		dir := filepath.Join(checkpointRoot(), c.ConvID, c.ID)
		for _, f := range files {
			if cerr := restoreOneFile(dir, f); cerr != nil {
				return services.FailMsg(fmt.Sprintf(
					"已回退 %d 步、还原 %d 个文件后失败：%v（撤销点已建立，可回滚本次回滚）",
					steps, len(done), cerr))
			}
			if !seen[f.Path] {
				seen[f.Path] = true
				done = append(done, f.Path)
			}
		}
		steps++
	}
	logger.I("[ai-checkpoint] 回滚 conv=%s 到 seq=%d 之前：撤 %d 步、还原 %d 个文件",
		convID, seq, steps, len(done))
	return services.Ok(map[string]any{
		"restored": len(done),
		"steps":    steps,
		"files":    done,
		"note":     "已回退到这一步之前（含其后所有步骤），消息未改动。本次回滚本身也可撤销：列表里最新那条是回滚前状态。",
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
	enforceCheckpointBudget(database)
	purgeAIApprovals(database)
}

// aiApprovalKeepDays 审批明细的保留天数。
//
// 90 天足够回答「最近这套闸门是不是太严」——那是按周看的判断，
// 再长的历史也不会改变结论，而这张表长得比用量表快（一次提问可能问好几次）。
const aiApprovalKeepDays = 90

// purgeAIApprovals 清掉过老的审批明细。
//
// 与回滚点清理同一趟做：都是「AI 自己的本地数据、只在启动时扫一遍」，
// 分成两条后台协程只会让启动多一次全表扫。
func purgeAIApprovals(database *db.Database) {
	if database == nil {
		return
	}
	n, err := database.PurgeAIApprovals(time.Now().AddDate(0, 0, -aiApprovalKeepDays))
	if err != nil {
		logger.W("[ai-approval] 清理审批明细失败: %v", err)
		return
	}
	if n > 0 {
		logger.I("[ai-approval] 已清理 %d 条过期审批明细", n)
	}
}

// enforceCheckpointBudget 盘占用超预算时按最旧优先淘汰。
//
// 条数上限管不住体积：一个 5MB 的文件改 20 次就是 100MB，三个这样的会话就超了，
// 而它们每一条单看都「还在允许的条数内」。上限放在这里而不是「拒绝新的快照」——
// 拒绝会让正在干活的 AI 忽然改不了文件，而用户并不知道原因。
//
// 每个会话至少留最新一条：全删干净的话，用户刚发现改坏了的那一刻，
// 唯一的退路正好被清理掉了。
func enforceCheckpointBudget(database *db.Database) {
	root := checkpointRoot()
	total := dirSize(root)
	if total <= aiCheckpointDiskBudget {
		return
	}
	list, err := database.ListAICheckpointsByAge()
	if err != nil {
		logger.W("[ai-checkpoint] 统计回滚点失败: %v", err)
		return
	}
	remain := make(map[string]int, len(list))
	for _, c := range list {
		remain[c.ConvID]++
	}
	var (
		ids   []string
		dirs  []string
		freed int64
	)
	for _, c := range list {
		if total-freed <= aiCheckpointDiskBudget {
			break
		}
		if remain[c.ConvID] <= 1 {
			continue
		}
		dir := filepath.Join(root, c.ConvID, c.ID)
		freed += dirSize(dir)
		ids = append(ids, c.ID)
		dirs = append(dirs, dir)
		remain[c.ConvID]--
	}
	if len(ids) == 0 {
		return
	}
	if _, derr := database.DeleteAICheckpointsByID(ids); derr != nil {
		logger.W("[ai-checkpoint] 淘汰回滚点失败: %v", derr)
		return
	}
	// 行删成功才删目录：反过来会留下「库里有行、文件没了」的假回滚点，
	// 用户点它会得到一个无法解释的失败。
	for _, d := range dirs {
		_ = os.RemoveAll(d)
	}
	logger.I("[ai-checkpoint] 盘占用超限，淘汰 %d 个最旧回滚点，释放 %d MB",
		len(ids), freed>>20)
}

// dirSize 目录下所有文件的字节数合计。读不到的部分按 0 算：
// 配额是尽力而为的治理，不该因为一个权限错误就让整轮清理停下。
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if fi, ierr := e.Info(); ierr == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}
