package sshx

// SFTP 文件操作的引擎层：只依赖一个已建立的 *ssh.Client，
// 不碰数据库、不碰纤程生命周期——那些是服务层的事。
//
// 分层的原因很实际：文件操作是最容易写出「连一次、忘释放」的地方，
// 把「拿到连接」和「用连接干活」拆开，前者由连接池统一管，
// 后者就是这里一堆可以吃单元测试的纯逻辑。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// 列表一次最多返回多少条。
//
// 这不是为了省内存：/proc、/sys 以及某些日志目录能有几万条目，
// 一次性序列化过 IPC 会把前端卡死几十秒。超了就截断并在 truncated 里告知，
// 让用户进子目录继续看——比让他干等有用得多。
const maxListEntries = 5000

// 文本文件预览的读取上限：够看配置文件，够不成把 2GB 日志塞进 JSON。
const defaultReadLimit = 1 << 20

var (
	// ErrRemoteNotExist 远端路径不存在。
	ErrRemoteNotExist = errors.New("sshx: 远端路径不存在")
	// ErrRemoteIsDir 期望是文件，实际是目录。
	ErrRemoteIsDir = errors.New("sshx: 该路径是目录")
	// ErrRemoteNotEmpty 目录非空，不能直接删。
	ErrRemoteNotEmpty = errors.New("sshx: 目录非空")
	// ErrInvalidRemotePath 路径不合法（为空、含非法字符、试图越界）。
	ErrInvalidRemotePath = errors.New("sshx: 路径无效")
	// ErrSftpUnavailable 服务端没开 SFTP 子系统（sshd_config 里缺
	// Subsystem sftp 一行，或 sftp-server 二进制不存在）。
	//
	// 值得单独给一个哨兵：这种情况 SSH 终端完全正常，用户会以为是
	// 「文件面板坏了」，实际需要在服务器上配 Subsystem。
	ErrSftpUnavailable = errors.New("sshx: 服务端未启用 SFTP 子系统")
)

// FileEntry 一条远端文件记录。
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
	IsLink  bool   `json:"isLink"`
	// LinkTo 符号链接指向的目标；非链接时为空串。
	LinkTo string `json:"linkTo"`
	Ext    string `json:"ext"`
}

// ListResult 一次目录列举的结果。
type ListResult struct {
	Path string `json:"path"`
	// Parent 上一级目录；已在根目录时为自身的 Clean 值。
	Parent    string      `json:"parent"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

// SftpSession 在一个已建立的 SSH 连接上执行文件操作。
//
// 用完必须 Close：它持有 SFTP 子系统的一条 channel，
// 这条 channel 不会因为底层 TCP 关闭而自动回收。
type SftpSession struct {
	client *sftp.Client
}

// NewSftpSession 在现有 SSH 连接上打开 SFTP 子系统。
//
// 刻意复用连接而不是另起一条：用户常常一边开着终端一边传文件，
// 各建一条意味着同一台主机占两个 TCP + 两次认证，服务端 sshd 的
// MaxSessions / fail2ban 阈值都更容易被踩到。
func NewSftpSession(conn *ssh.Client) (*SftpSession, error) {
	if conn == nil {
		return nil, errors.New("sshx: ssh 连接为空")
	}
	c, err := sftp.NewClient(conn)
	if err != nil {
		// 打开失败几乎只有两种原因：服务端禁了 Subsystem sftp，
		// 或者它给了协议握手错误。无论哪种，用户端的操作界面都一样：文件面板不可用。
		return nil, fmt.Errorf("%w: %v", ErrSftpUnavailable, err)
	}
	return &SftpSession{client: c}, nil
}

// Close 关闭 SFTP 子系统。底层 SSH 连接不受影响。
func (s *SftpSession) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// RealPath 解析成规范绝对路径。空路径等价于远端登录后的家目录。
//
// 必须走远端的 RealPath：家目录到底在哪（`~`、相对路径、符号链接的 pwd）
// 只有服务端说了算，本地猜会让柱面的面包屑与实际目录对不上。
func (s *SftpSession) RealPath(remotePath string) (string, error) {
	p := CleanRemotePath(remotePath)
	if p == "" {
		p = "."
	}
	abs, err := s.client.RealPath(p)
	if err != nil {
		return "", classifySftpErr(remotePath, err)
	}
	return abs, nil
}

// CleanRemotePath 归一化远端路径。
//
// 处理三件事：波浪号（只允许单独的 ~ 或 ~/ 开头）、多余的斜杠、. 与 ..。
// 越界到根目录以上的 .. 会被压到 /——我们不想让面板出现 "/../etc" 这种展示。
func CleanRemotePath(remotePath string) string {
	p := strings.TrimSpace(remotePath)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = "." + strings.TrimPrefix(p, "~")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") {
		p = "./" + p
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// List 列举目录内容。目录排在前面，其余按名称排。
func (s *SftpSession) List(remotePath string) (ListResult, error) {
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return ListResult{}, err
	}

	infos, err := s.client.ReadDir(abs)
	if err != nil {
		return ListResult{Path: abs}, classifySftpErr(abs, err)
	}

	res := ListResult{
		Path:    abs,
		Parent:  parentOf(abs),
		Entries: make([]FileEntry, 0, len(infos)),
	}

	// 超限时「先取满再排序」会漏掉目录——排序后截断则能优先保住文件名靠前的
	// 目录，两者的实际观感差别很大（后者的列表仍能用）。
	if len(infos) > maxListEntries {
		res.Truncated = true
		infos = infos[:maxListEntries]
	}

	for _, info := range infos {
		res.Entries = append(res.Entries, buildEntry(abs, info))
	}
	sortEntries(res.Entries)
	return res, nil
}

// Stat 查询单个路径的属性。
func (s *SftpSession) Stat(remotePath string) (FileEntry, error) {
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return FileEntry{}, err
	}
	info, err := s.client.Stat(abs)
	if err != nil {
		return FileEntry{}, classifySftpErr(abs, err)
	}
	return buildEntry(parentOf(abs), info), nil
}

// Mkdir 创建目录，连带创建缺失的中间层。
func (s *SftpSession) Mkdir(remotePath string) error {
	clean := CleanRemotePath(remotePath)
	if clean == "" {
		return fmt.Errorf("%w: 目录名为空", ErrInvalidRemotePath)
	}

	// 相对路径以家目录为基准，必须先问服务端家目录在哪。
	// 不能用 RealPath(parent) 去反推：那样一旦中间层还不存在，
	// RealPath 自己就先报 ErrRemoteNotExist，MkdirAll 的
	// 「连中间层一起建」能力等于作废——用户想建 /opt/a/b/c 就得先手动建到 /opt/a/b。
	target := clean
	if !strings.HasPrefix(target, "/") {
		home, err := s.RealPath("")
		if err != nil {
			return err
		}
		target = path.Join(home, clean)
	}

	if err := s.client.MkdirAll(target); err != nil {
		return classifySftpErr(target, err)
	}
	return nil
}

// isRootPath 判断路径是不是文件系统根。
//
// 递归删除前必须过这一关：文件面板的面包屑第一项就是 /，
// 「点两下面包屑再点删除」恰好是整台服务器，这是所有不可恢复操作里
// 唯一一个「一次点击毁掉全部」的，必须先堵死。
//
// 只认「Clean 之后恰为一个斜杠」这一种形态。不用 RealPath 的结果做
// 字符串比较：它的返回形态取决于服务端实现（带 chroot 前缀的会变成
// /E:/ 这样），靠不住。
func isRootPath(p string) bool {
	return path.Clean(p) == "/"
}

// Remove 删除文件或目录。recursive 为假且目标是非空目录时报错，
// 绝不静默递归——误删整棵目录树是不可逆的。
func (s *SftpSession) Remove(remotePath string, recursive bool) error {
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return err
	}
	if isRootPath(abs) {
		return fmt.Errorf("%w: 不允许删除根目录", ErrInvalidRemotePath)
	}

	info, err := s.client.Stat(abs)
	if err != nil {
		return classifySftpErr(abs, err)
	}

	if !info.IsDir() {
		if err := s.client.Remove(abs); err != nil {
			return classifySftpErr(abs, err)
		}
		return nil
	}

	if !recursive {
		// 这里有个刻意的取舍：先 ReadDir 再删，中间存在极小的时间窗，
		// 期间若有并发写入会漏删。但换来的是「目录非空」这个明确的错误码——
		// 直接删的话服务端只给一个语焉不详的 SSH_FX_FAILURE，
		// 用户看到的就是「操作失败」，完全不知道该先清空目录。
		children, err := s.client.ReadDir(abs)
		if err != nil {
			return classifySftpErr(abs, err)
		}
		if len(children) > 0 {
			return fmt.Errorf("%w: %s", ErrRemoteNotEmpty, abs)
		}
		if err := s.client.RemoveDirectory(abs); err != nil {
			return classifySftpErr(abs, err)
		}
		return nil
	}

	// 先删子节点再删父：Walk 是前序遍历，倒着走才能保证子先于父。
	//
	// 遍历时就把 isDir 记下来：Walker 给的 FileInfo 是已经在手的，
	// 不记的话删除阶段只能靠「先 Remove、失败再 RemoveDirectory」去猜，
	// 每删一个目录就要多一次往返——删一棵上千节点的树会翻倍。
	type victim struct {
		path  string
		isDir bool
	}
	var victims []victim
	w := s.client.Walk(abs)
	for w.Step() {
		if w.Err() != nil {
			return classifySftpErr(w.Path(), w.Err())
		}
		victims = append(victims, victim{path: w.Path(), isDir: w.Stat().IsDir()})
	}

	for i := len(victims) - 1; i >= 0; i-- {
		v := victims[i]
		if v.isDir {
			if err := s.client.RemoveDirectory(v.path); err != nil {
				return classifySftpErr(v.path, err)
			}
			continue
		}
		if err := s.client.Remove(v.path); err != nil {
			return classifySftpErr(v.path, err)
		}
	}
	return nil
}

// Rename 移动或重命名。同分区内的 rename 是原子的，跨设备由服务端处理。
func (s *SftpSession) Rename(oldPath, newPath string) error {
	oldAbs, err := s.RealPath(oldPath)
	if err != nil {
		return err
	}
	newClean := CleanRemotePath(newPath)
	if newClean == "" {
		return fmt.Errorf("%w: 目标路径为空", ErrInvalidRemotePath)
	}
	if !strings.HasPrefix(newClean, "/") {
		// 相对目标按源文件所在目录解释，与 shell 的 mv 行为一致。
		newClean = path.Join(parentOf(oldAbs), newClean)
	}
	if err := s.client.Rename(oldAbs, newClean); err != nil {
		return classifySftpErr(newClean, err)
	}
	return nil
}

// ReadText 读取文本文件的前 limit 字节并转成字符串。
//
// 二进制文件不做判定：用户主动查看就该看到原样内容，
// 由前端负责在渲染时替换不可见字符。
func (s *SftpSession) ReadText(remotePath string, limit int64) (string, error) {
	if limit <= 0 {
		limit = defaultReadLimit
	}
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return "", err
	}
	info, err := s.client.Stat(abs)
	if err != nil {
		return "", classifySftpErr(abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrRemoteIsDir, abs)
	}

	f, err := s.client.Open(abs)
	if err != nil {
		return "", classifySftpErr(abs, err)
	}
	defer f.Close()

	size := info.Size()
	if size > limit {
		size = limit
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("sshx: 读取远端文件失败: %w", err)
	}
	return string(buf[:n]), nil
}

// WriteText 覆盖写入文本文件。
//
// mode 为 0 时不改权限：场景是「改一个已存在的配置文件」，
// 把 nginx.conf 从 600 改成 644 属于意外的副作用。
func (s *SftpSession) WriteText(remotePath, content string, mode os.FileMode) error {
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return err
	}
	f, err := s.client.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return classifySftpErr(abs, err)
	}
	defer f.Close()

	if _, err := f.Write([]byte(content)); err != nil {
		return fmt.Errorf("sshx: 写入远端文件失败: %w", err)
	}
	if mode != 0 {
		if err := f.Chmod(mode); err != nil {
			return fmt.Errorf("sshx: 设置远端权限失败: %w", err)
		}
	}
	return nil
}

// Download 把远端文件或目录拉到本地。
//
// dst 落在已存在的本地目录时按源文件名拼到它下面；否则当作完整目标路径，
// 覆盖同名文件。文件面板里用户期待的就是「下载下来替换掉」，
// 下载要能和 server 上正在被 tail -f 的路径对上。
func (s *SftpSession) Download(remotePath, dst string, onProgress func(done, total int64)) error {
	abs, err := s.RealPath(remotePath)
	if err != nil {
		return err
	}
	info, err := s.client.Stat(abs)
	if err != nil {
		return classifySftpErr(abs, err)
	}

	if info.IsDir() {
		return s.downloadDir(abs, dst, onProgress)
	}
	local, err := chooseLocalPath(dst, path.Base(abs))
	if err != nil {
		return err
	}
	return s.downloadFile(abs, local, info.Size(), onProgress)
}

// Upload 把本地文件上传到远端，remoteDir 为已存在的目标目录。
func (s *SftpSession) Upload(localPath, remoteDir string, onProgress func(done, total int64)) error {
	local, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("sshx: 读取本地文件失败: %w", err)
	}
	dir, err := s.RealPath(remoteDir)
	if err != nil {
		return err
	}
	return s.uploadFile(localPath, path.Join(dir, filepath.Base(localPath)), local.Size(), onProgress)
}

func (s *SftpSession) downloadFile(remotePath, localPath string, size int64, onProgress func(done, total int64)) error {
	src, err := s.client.Open(remotePath)
	if err != nil {
		return classifySftpErr(remotePath, err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("sshx: 创建本地目录失败: %w", err)
	}
	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("sshx: 创建本地文件失败: %w", err)
	}
	defer dst.Close()

	if _, err := copyTrack(src, dst, size, onProgress); err != nil {
		return fmt.Errorf("sshx: 下载失败: %w", err)
	}
	return nil
}

func (s *SftpSession) downloadDir(remotePath, localRoot string, onProgress func(done, total int64)) error {
	w := s.client.Walk(remotePath)
	for w.Step() {
		if w.Err() != nil {
			return classifySftpErr(w.Path(), w.Err())
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(w.Path(), remotePath), "/")
		target := filepath.Join(localRoot, filepath.FromSlash(rel))
		// 远端返回的名字里可能带 ..（恶意服务端或被人放了奇怪文件名的目录），
		// Clean 之后仍能逃出 localRoot。落盘前必须再验一次。
		if !withinRoot(localRoot, target) {
			return fmt.Errorf("%w: 远端路径 %q 越出了下载目录", ErrInvalidRemotePath, w.Path())
		}
		info := w.Stat()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("sshx: 创建本地目录失败: %w", err)
			}
			continue
		}
		// 复用这一次 Stat 的结果，不要到 downloadFile 里再 Stat 一遍。
		if err := s.downloadFile(w.Path(), target, info.Size(), onProgress); err != nil {
			return err
		}
	}
	return nil
}

func (s *SftpSession) uploadFile(localPath, remotePath string, size int64, onProgress func(done, total int64)) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("sshx: 打开本地文件失败: %w", err)
	}
	defer src.Close()

	dst, err := s.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return classifySftpErr(remotePath, err)
	}
	defer dst.Close()

	if _, err := copyTrack(src, dst, size, onProgress); err != nil {
		return fmt.Errorf("sshx: 上传失败: %w", err)
	}
	return nil
}

// copyTrack 带进度的拷贝。32KB 的缓冲区是 SFTP 的常见最优窗口：
// 再大会让单帧超过 SSH 通道窗口，反而要等 ACK。
func copyTrack(src io.Reader, dst io.Writer, total int64, onProgress func(done, total int64)) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			written += int64(w)
			if werr != nil {
				return written, werr
			}
			if w != n {
				return written, io.ErrShortWrite
			}
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

// chooseLocalPath dst 为已存在的目录时，把文件名拼到它下面。
//
// 文件名必须先过 sanitizeLocalName：远端是 POSIX 语义，`\` 只是普通字符，
// 但 Windows 的 filepath.Join 会把它当分隔符再 Clean，于是远端一个名叫
// `..\..\..\Startup\evil.exe` 的文件能把下载落到目标目录之外（zip-slip）。
func chooseLocalPath(dst, base string) (string, error) {
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		name, err := sanitizeLocalName(base)
		if err != nil {
			return "", err
		}
		return filepath.Join(dst, name), nil
	}
	return dst, nil
}

// sanitizeLocalName 把远端文件名收敛成本地可安全落盘的名字。
//
// 拒掉的都是「在远端合法、在本地有歧义」的字符：路径分隔符、Windows 保留
// 设备名、控制字符、结尾的点与空格（Windows 会静默剥离，导致 a.conf 与
// "a.conf " 撞车成同一个文件）。宁可让一次下载失败并给出明确原因，
// 也不能静默写错位置。
func sanitizeLocalName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || n == "." || n == ".." {
		return "", fmt.Errorf("%w: 远端文件名为空或非法", ErrInvalidRemotePath)
	}
	// Windows 上这些字符/名字会打到本地设备名或被静默改写，直接拒绝。
	// 非 Windows 平台它们是完全合法的文件名，不能一并拦掉。
	if runtime.GOOS == "windows" {
		for _, r := range n {
			if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
				r == '"' || r == '<' || r == '>' || r == '|' || r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("%w: 文件名含非法字符 %q", ErrInvalidRemotePath, string(r))
			}
		}
		if isWindowsReservedName(n) {
			return "", fmt.Errorf("%w: 文件名是 Windows 保留名 %q", ErrInvalidRemotePath, n)
		}
		n = strings.TrimRight(n, ". ")
		if n == "" {
			return "", fmt.Errorf("%w: 远端文件名为空或非法", ErrInvalidRemotePath)
		}
	}
	return n, nil
}

// isWindowsReservedName 判断是否是 CON/PRN/AUX/NUL/COM1-9/LPT1-9。
// 带扩展名也算（CON.txt 同样指向设备），所以按主干名比对。
func isWindowsReservedName(name string) bool {
	stem := name
	if i := strings.LastIndex(stem, "."); i > 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) == 4 {
		prefix := strings.ToUpper(stem[:3])
		if prefix == "COM" || prefix == "LPT" {
			d := stem[3]
			return d >= '1' && d <= '9'
		}
	}
	return false
}

// withinRoot 校验拼出来的本地路径确实落在 root 之内。
//
// 不能只依赖 filepath.Join 的 Clean 语义——它无法防住「远端返回的名字里
// 自带 ..」这种情况。这是最后一道闸，凡是要落盘的本地路径都该过一遍。
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parentOf 上一级目录。根目录返回自身，避免面包屑出现空的上一层。
func parentOf(abs string) string {
	p := path.Dir(path.Clean(abs))
	if p == "" {
		return abs
	}
	return p
}

func buildEntry(dir string, info os.FileInfo) FileEntry {
	name := info.Name()
	isLink := info.Mode()&os.ModeSymlink != 0
	e := FileEntry{
		Name:    name,
		Path:    path.Join(dir, name),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Unix(),
		IsDir:   info.IsDir(),
		IsLink:  isLink,
	}
	if !e.IsDir {
		e.Ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	}
	return e
}

// sortEntries 目录优先，其次按名称。大小写敏感会让 "Zebra" 排在 "apple" 之前，
// 视觉上跳来跳去，所以统一按折叠后的键排序。
func sortEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		ka, kb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if ka != kb {
			return ka < kb
		}
		return a.Name < b.Name
	})
}

// classifySftpErr 把 SFTP 的原始错误收敛成引擎层的哨兵错误，
// 服务层再映射成稳定错误码——前端不必认识 os.ErrNotExist。
//
// 判断顺序：先认 os.ErrNotExist（本地 symlink 判定会直接给出），
// 再认 SFTP 自己的 fx code，最后是纯文本兜底。
func classifySftpErr(p string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrRemoteNotExist, p)
	}

	var sErr *sftp.StatusError
	if errors.As(err, &sErr) {
		switch sErr.FxCode() {
		case sftp.ErrSSHFxNoSuchFile:
			return fmt.Errorf("%w: %s", ErrRemoteNotExist, p)
		case sftp.ErrSSHFxPermissionDenied:
			return fmt.Errorf("sshx: 没有权限访问 %s: %w", p, err)
		case sftp.ErrSSHFxFailure:
			return fmt.Errorf("sshx: 服务端拒绝了对 %s 的操作: %w", p, err)
		}
	}

	// "file does not exist" 这类错误有的服务端不带 fx code，只能看文本。
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist") {
		return fmt.Errorf("%w: %s", ErrRemoteNotExist, p)
	}
	return fmt.Errorf("sshx: sftp 操作失败: %w", err)
}
