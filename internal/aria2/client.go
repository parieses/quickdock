package aria2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// Client 是 aria2 JSON-RPC over HTTP 的薄封装。
// Base / WSBase 由 Config 统一拼装：本机走回环，远程可走 http(s)/ws(s)。
type Client struct {
	Port   int
	Secret string
	Base   string
	WSBase string
	HTTP   *http.Client
}

// readMethods 是只读且幂等的 RPC 方法：偶发网络超时可以安全重试一次。
// ⚠️ 绝不要把 addUri / remove / pause / changeOption / shutdown 加进来——
// 重试写方法会重复建任务、重复删任务，破坏力比一次超时大得多。
var readMethods = map[string]bool{
	"aria2.getVersion":      true,
	"aria2.tellActive":      true,
	"aria2.tellWaiting":     true,
	"aria2.tellStopped":     true,
	"aria2.tellStatus":      true,
	"aria2.getGlobalStat":   true,
	"aria2.getGlobalOption": true,
	"aria2.getOption":       true,
	"aria2.getFiles":        true,
	"aria2.getUris":         true,
	"aria2.getPeers":        true,
	"aria2.getServers":      true,
	"aria2.getSessionInfo":  true,
	"system.listMethods":    true,
}

// statusKeys 显式列出 tell* 需要的字段：不给 keys 时 aria2 会连几十 KB 的
// bitfield / 完整 files 一起返回，BT 任务更夸张，直接拖慢 1s 的进度轮询。
var statusKeys = []string{
	"gid", "status", "totalLength", "completedLength", "uploadLength",
	"downloadSpeed", "uploadSpeed", "dir", "errorMessage", "files",
	"numSeeders", "seeder", "connections", "infoHash", "numPieces",
	"pieceLength", "bittorrent",
}

// transportError 只包网络层失败（连接、等待响应头、读 body）。
// aria2 明确返回的业务错误（-32602 参数非法等）不该重试。
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func NewClient(c *Config) *Client {
	// 远程要跨网络，拨号上限放宽到 5s；本机保留 2s。
	dialTimeout := 2 * time.Second
	noKeepAlive := true
	if c.IsRemote() {
		dialTimeout = 5 * time.Second
		// 远程常经 Nginx 反代（连接复用是正常的），每次都重建 TLS 反而拖慢 1s 轮询。
		noKeepAlive = false
	}
	return &Client{
		Port:   c.RPCPort(),
		Secret: c.RPCSecret(),
		Base:   c.RPCBase(),
		WSBase: c.WSBase(),
		HTTP: &http.Client{
			// 5s 太激进：daemon 正在写几十 GB 文件或保存会话时，单线程 RPC 可能排队数秒。
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ResponseHeaderTimeout: 12 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				MaxIdleConnsPerHost:   2,
				// aria2 1.37 的回环 RPC 是「一连接一请求」语义，成功响应却不发
				// Connection: close；Go 复用已被对端关掉的 socket 会一直等到整体超时。
				DisableKeepAlives: noKeepAlive,
			},
		},
	}
}

// callRaw 不加 token 的底层调用；只读方法遇到网络层失败自动重试一次，写方法一次就是一次。
func (c *Client) callRaw(method string, params []any) (json.RawMessage, error) {
	attempts := 1
	if readMethods[method] {
		attempts = 2
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(120 * time.Millisecond)
		}
		out, err := c.callOnce(method, params)
		if err == nil {
			return out, nil
		}
		lastErr = err
		var te *transportError
		if !errors.As(err, &te) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) callOnce(method string, params []any) (json.RawMessage, error) {
	if c == nil || c.Base == "" {
		return nil, fmt.Errorf("aria2 RPC 未配置")
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: "quickdock", Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.Base, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &transportError{fmt.Errorf("aria2 daemon 未响应: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("aria2 RPC HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &transportError{err}
	}
	var out rpcResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("解析 aria2 响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("aria2 %s 失败(%d): %s", method, out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

// Call 自动在参数最前面补 token:<secret>（aria2 的 RPC 鉴权约定）。
// 远程 aria2 可能没设 secret，此时不能带 token 前缀，否则第一个参数会被误读。
func (c *Client) Call(method string, params ...any) (json.RawMessage, error) {
	if c.Secret != "" {
		params = append([]any{"token:" + c.Secret}, params...)
	}
	return c.callRaw(method, params)
}

// ===== system.multicall =====

// MethodCall 是 system.multicall 里的一个子调用。
// token 必须放进每个子调用的 params，而不是 multicall 自己的 params——
// aria2 按 methodCall 逐个校验鉴权，放外层会让每个子调用都报 "Unauthorized"。
type MethodCall struct {
	MethodName string `json:"methodName"`
	Params     []any  `json:"params"`
}

type multiCallFault struct {
	FaultCode   int    `json:"faultCode"`
	FaultString string `json:"faultString"`
}

// MultiCallResult 是单个子调用的结果。整个 RPC 返回 200 不代表每项都成功：
// aria2 把失败项以 fault 对象混在 result 数组里，其余位置仍是正常结果。
type MultiCallResult struct {
	OK    bool
	Raw   json.RawMessage
	Error string
}

// MultiCall 一次 HTTP 往返执行多个 aria2 方法。
// 刻意不进 readMethods：批量里常混写方法，网络抖动重试会重复执行已经生效的那一半。
func (c *Client) MultiCall(calls []MethodCall) ([]MultiCallResult, error) {
	if len(calls) == 0 {
		return []MultiCallResult{}, nil
	}
	wire := make([]MethodCall, 0, len(calls))
	for _, call := range calls {
		params := call.Params
		if c.Secret != "" {
			params = append([]any{"token:" + c.Secret}, params...)
		}
		wire = append(wire, MethodCall{MethodName: call.MethodName, Params: params})
	}
	raw, err := c.callRaw("system.multicall", []any{wire})
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("解析 multicall 响应失败: %w", err)
	}
	out := make([]MultiCallResult, 0, len(items))
	for _, item := range items {
		// 成功项可能是数组（["gid"]）也可能是对象（tellStatus 的状态对象），
		// 只有带 faultCode 键的对象才是失败；不能靠「能不能解析成对象」判断。
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err == nil {
			if _, hasFault := obj["faultCode"]; hasFault {
				var fault multiCallFault
				_ = json.Unmarshal(item, &fault)
				msg := fault.FaultString
				if msg == "" {
					msg = fmt.Sprintf("aria2 fault %d", fault.FaultCode)
				}
				out = append(out, MultiCallResult{Error: msg})
				continue
			}
		}
		out = append(out, MultiCallResult{OK: true, Raw: item})
	}
	return out, nil
}

// ===== 引擎 / 会话 =====

func (c *Client) GetVersion() (string, error) {
	raw, err := c.Call("aria2.getVersion")
	if err != nil {
		return "", err
	}
	var v struct {
		Version         string   `json:"version"`
		EnabledFeatures []string `json:"enabledFeatures"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// VersionInfo 给设置页显示引擎编译特性（是否带 BT / 异步 DNS 等）。
type VersionInfo struct {
	Version         string   `json:"version"`
	EnabledFeatures []string `json:"enabledFeatures"`
}

func (c *Client) GetVersionInfo() (VersionInfo, error) {
	raw, err := c.Call("aria2.getVersion")
	if err != nil {
		return VersionInfo{}, err
	}
	var v VersionInfo
	err = json.Unmarshal(raw, &v)
	return v, err
}

func (c *Client) GetSessionInfo() (SessionInfo, error) {
	raw, err := c.Call("aria2.getSessionInfo")
	if err != nil {
		return SessionInfo{}, err
	}
	var v SessionInfo
	err = json.Unmarshal(raw, &v)
	return v, err
}

type SessionInfo struct {
	SessionID string `json:"sessionId"`
}

// ListMethods 让设置页能显示「当前 daemon 支持哪些 RPC 方法」，纯诊断用途。
func (c *Client) ListMethods() ([]string, error) {
	raw, err := c.callRaw("system.listMethods", []any{})
	if err != nil {
		return nil, err
	}
	var out []string
	err = json.Unmarshal(raw, &out)
	return out, err
}

// SaveSession 让 aria2 立刻把当前任务列表写回 --save-session 文件。
func (c *Client) SaveSession() error {
	_, err := c.Call("aria2.saveSession")
	return err
}

// Shutdown 优雅关闭；force=true 用 forceShutdown。
// 只由「关闭引擎」显式调用，DlDisable 仍走环境层的 Stop（要回收进程、刷检测缓存）。
func (c *Client) Shutdown(force bool) error {
	method := "aria2.shutdown"
	if force {
		method = "aria2.forceShutdown"
	}
	_, err := c.Call(method)
	return err
}

// ===== 新建任务 =====

func (c *Client) AddURI(uris []string, opts map[string]string) (string, error) {
	return c.AddURIAt(uris, opts, 0)
}

// AddURIAt 的 position>0 表示插到等待队列的第 N 位（aria2 的 position 参数）。
func (c *Client) AddURIAt(uris []string, opts map[string]string, position int) (string, error) {
	params := []any{uris, opts}
	if position > 0 {
		params = append(params, position)
	}
	raw, err := c.Call("aria2.addUri", params...)
	if err != nil {
		return "", err
	}
	var gid string
	err = json.Unmarshal(raw, &gid)
	return gid, err
}

// AddTorrent 的 torrent 是 .torrent 文件内容的 base64；uris 是可选的 web-seeding 地址。
func (c *Client) AddTorrent(torrent string, uris []string, opts map[string]string, position int) (string, error) {
	if uris == nil {
		uris = []string{}
	}
	params := []any{torrent, uris, opts}
	if position > 0 {
		params = append(params, position)
	}
	raw, err := c.Call("aria2.addTorrent", params...)
	if err != nil {
		return "", err
	}
	var gid string
	err = json.Unmarshal(raw, &gid)
	return gid, err
}

// AddMetalink 的 metalink 是 .metalink/.meta4 文件内容的 base64，一次可能产出多个 GID。
func (c *Client) AddMetalink(metalink string, opts map[string]string, position int) ([]string, error) {
	params := []any{metalink, opts}
	if position > 0 {
		params = append(params, position)
	}
	raw, err := c.Call("aria2.addMetalink", params...)
	if err != nil {
		return nil, err
	}
	var gids []string
	err = json.Unmarshal(raw, &gids)
	return gids, err
}

// ===== 任务控制 =====

func (c *Client) Pause(gid string) error {
	_, err := c.Call("aria2.pause", gid)
	return err
}

func (c *Client) ForcePause(gid string) error {
	_, err := c.Call("aria2.forcePause", gid)
	return err
}

func (c *Client) Unpause(gid string) error {
	_, err := c.Call("aria2.unpause", gid)
	return err
}

func (c *Client) PauseAll(force bool) error {
	method := "aria2.pauseAll"
	if force {
		method = "aria2.forcePauseAll"
	}
	_, err := c.Call(method)
	return err
}

func (c *Client) UnpauseAll() error {
	_, err := c.Call("aria2.unpauseAll")
	return err
}

// ChangePosition 调整等待队列位置，how 取 POS_SET / POS_CUR / POS_END。
func (c *Client) ChangePosition(gid string, pos int, how string) (int, error) {
	raw, err := c.Call("aria2.changePosition", gid, pos, how)
	if err != nil {
		return 0, err
	}
	var out int
	err = json.Unmarshal(raw, &out)
	return out, err
}

// ChangeURI 增删某个文件的下载源，返回 [删除数, 新增数]。
func (c *Client) ChangeURI(gid string, fileIndex int, delUris, addUris []string) ([]int, error) {
	raw, err := c.Call("aria2.changeUri", gid, fileIndex, delUris, addUris)
	if err != nil {
		return nil, err
	}
	var out []int
	err = json.Unmarshal(raw, &out)
	return out, err
}

// StatusOf 只向 aria2 要 status 一个字段。
func (c *Client) StatusOf(gid string) (string, error) {
	raw, err := c.Call("aria2.tellStatus", gid, []string{"status"})
	if err != nil {
		return "", err
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return "", err
	}
	return st.Status, nil
}

// RemoveDownloadResult 从「已结束结果列表」里清掉一条记录。
func (c *Client) RemoveDownloadResult(gid string) error {
	_, err := c.Call("aria2.removeDownloadResult", gid)
	return err
}

func (c *Client) PurgeDownloadResult() error {
	_, err := c.Call("aria2.purgeDownloadResult")
	return err
}

// Remove 移除任务。aria2 把「活跃/等待/暂停」与「已结束的结果」分成两套 API：
// remove / forceRemove 只作用于前者，对 complete / error / removed 一律回
// "Active Download not found"；后者必须用 removeDownloadResult。
func (c *Client) Remove(gid string, force bool) error {
	st, err := c.StatusOf(gid)
	if err != nil {
		if isGIDNotFound(err) {
			return nil
		}
		return err
	}
	switch st {
	case "complete", "error", "removed":
		return c.RemoveDownloadResult(gid)
	default:
		method := "aria2.remove"
		if force {
			method = "aria2.forceRemove"
		}
		_, err := c.Call(method, gid)
		return err
	}
}

func isGIDNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// ===== 批量操作 =====

func gidCalls(method string, gids []string) []MethodCall {
	out := make([]MethodCall, 0, len(gids))
	for _, gid := range gids {
		out = append(out, MethodCall{MethodName: method, Params: []any{gid}})
	}
	return out
}

func (c *Client) PauseMany(gids []string) ([]MultiCallResult, error) {
	return c.MultiCall(gidCalls("aria2.pause", gids))
}

func (c *Client) UnpauseMany(gids []string) ([]MultiCallResult, error) {
	return c.MultiCall(gidCalls("aria2.unpause", gids))
}

// RemoveMany 固定两次往返：先批量问状态，再按状态分派到两套删除 API。
// 逐条调用是 2N 次（每个 gid 先 tellStatus 再 remove），选中几十个任务时
// 串行等待会明显卡住界面；这里无论 N 多大都是 2 次。
func (c *Client) RemoveMany(gids []string, force bool) ([]MultiCallResult, error) {
	if len(gids) == 0 {
		return []MultiCallResult{}, nil
	}
	statusCalls := make([]MethodCall, 0, len(gids))
	for _, gid := range gids {
		statusCalls = append(statusCalls, MethodCall{
			MethodName: "aria2.tellStatus", Params: []any{gid, []string{"status"}},
		})
	}
	statuses, err := c.MultiCall(statusCalls)
	if err != nil {
		return nil, err
	}

	delCalls := make([]MethodCall, 0, len(gids))
	tracked := make([]int, 0, len(gids)) // delCalls[j] 对应 gids[tracked[j]]
	for i, gid := range gids {
		st := ""
		if i < len(statuses) {
			if statuses[i].OK {
				var v struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(statuses[i].Raw, &v)
				st = v.Status
			} else if isGIDNotFound(errors.New(statuses[i].Error)) {
				// 任务已不存在的，与单条 Remove 保持一致：直接算成功，不再发删除指令。
				continue
			}
		}
		method := "aria2.remove"
		if force {
			method = "aria2.forceRemove"
		}
		switch st {
		case "complete", "error", "removed":
			method = "aria2.removeDownloadResult"
		}
		delCalls = append(delCalls, MethodCall{MethodName: method, Params: []any{gid}})
		tracked = append(tracked, i)
	}
	if len(delCalls) == 0 {
		out := make([]MultiCallResult, len(gids))
		for i := range out {
			out[i] = MultiCallResult{OK: true}
		}
		return out, nil
	}
	deleted, err := c.MultiCall(delCalls)
	if err != nil {
		return nil, err
	}
	out := make([]MultiCallResult, len(gids))
	for i := range out {
		out[i] = MultiCallResult{OK: true}
	}
	for j, idx := range tracked {
		if j < len(deleted) {
			out[idx] = deleted[j]
		} else {
			out[idx] = MultiCallResult{Error: "aria2 未返回该任务的删除结果"}
		}
	}
	return out, nil
}

// ===== 选项 =====

func (c *Client) ChangeGlobalOption(opts map[string]string) error {
	_, err := c.Call("aria2.changeGlobalOption", opts)
	return err
}

func (c *Client) GetGlobalOption() (map[string]string, error) {
	raw, err := c.Call("aria2.getGlobalOption")
	if err != nil {
		return nil, err
	}
	var out map[string]string
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *Client) GetOption(gid string) (map[string]string, error) {
	raw, err := c.Call("aria2.getOption", gid)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *Client) ChangeOption(gid string, opts map[string]string) error {
	_, err := c.Call("aria2.changeOption", gid, opts)
	return err
}

// ===== 状态与详情 =====

func (c *Client) TellStatus(gid string) (Status, error) {
	raw, err := c.Call("aria2.tellStatus", gid, statusKeys)
	if err != nil {
		return Status{}, err
	}
	var st Status
	err = json.Unmarshal(raw, &st)
	return st, err
}

// RawFile / RawURI / RawPeer / RawServer 是 aria2 的协议对象：所有数值都是十进制字符串，
// selected / seeder 这类布尔也是 "true"/"false" 字符串。视图转换统一放在 services/download。
type RawFile struct {
	Index           string   `json:"index"`
	Path            string   `json:"path"`
	Length          string   `json:"length"`
	CompletedLength string   `json:"completedLength"`
	Selected        string   `json:"selected"`
	URIs            []RawURI `json:"uris"`
}

type RawURI struct {
	URI    string `json:"uri"`
	Status string `json:"status"`
}

type RawPeer struct {
	PeerID        string `json:"peerId"`
	IP            string `json:"ip"`
	Port          string `json:"port"`
	Bitfield      string `json:"bitfield"`
	AmChoking     string `json:"amChoking"`
	PeerChoking   string `json:"peerChoking"`
	DownloadSpeed string `json:"downloadSpeed"`
	UploadSpeed   string `json:"uploadSpeed"`
	Seeder        string `json:"seeder"`
}

type RawServer struct {
	Index   string `json:"index"`
	Servers []struct {
		URI           string `json:"uri"`
		CurrentURI    string `json:"currentUri"`
		DownloadSpeed string `json:"downloadSpeed"`
		Status        string `json:"status"`
		ErrorMessage  string `json:"errorMessage"`
	} `json:"servers"`
}

func (c *Client) GetFiles(gid string) ([]RawFile, error) {
	raw, err := c.Call("aria2.getFiles", gid)
	if err != nil {
		return nil, err
	}
	var out []RawFile
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *Client) GetUris(gid string) ([]RawURI, error) {
	raw, err := c.Call("aria2.getUris", gid)
	if err != nil {
		return nil, err
	}
	var out []RawURI
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *Client) GetPeers(gid string) ([]RawPeer, error) {
	raw, err := c.Call("aria2.getPeers", gid)
	if err != nil {
		return nil, err
	}
	var out []RawPeer
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (c *Client) GetServers(gid string) ([]RawServer, error) {
	raw, err := c.Call("aria2.getServers", gid)
	if err != nil {
		return nil, err
	}
	var out []RawServer
	err = json.Unmarshal(raw, &out)
	return out, err
}

type GlobalStat struct {
	DownloadSpeed   string `json:"downloadSpeed"`
	UploadSpeed     string `json:"uploadSpeed"`
	NumActive       string `json:"numActive"`
	NumWaiting      string `json:"numWaiting"`
	NumStopped      string `json:"numStopped"`
	NumStoppedTotal string `json:"numStoppedTotal"`
}

func (c *Client) GetGlobalStat() (GlobalStat, error) {
	raw, err := c.Call("aria2.getGlobalStat")
	if err != nil {
		return GlobalStat{}, err
	}
	var out GlobalStat
	err = json.Unmarshal(raw, &out)
	return out, err
}

// Status 是 aria2.tellStatus / tellActive 返回的单任务快照。
type Status struct {
	GID             string `json:"gid"`
	Status          string `json:"status"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	UploadLength    string `json:"uploadLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	UploadSpeed     string `json:"uploadSpeed"`
	ErrorMessage    string `json:"errorMessage"`
	Dir             string `json:"dir"`
	NumSeeders      string `json:"numSeeders"`
	Seeder          string `json:"seeder"`
	Connections     string `json:"connections"`
	InfoHash        string `json:"infoHash"`
	NumPieces       string `json:"numPieces"`
	PieceLength     string `json:"pieceLength"`
	Bitfield        string `json:"bitfield"`
	Bittorrent      *struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
		AnnounceList [][]string `json:"announceList"`
		Comment      string     `json:"comment"`
		CreationDate int64      `json:"creationDate"`
		Mode         string     `json:"mode"`
	} `json:"bittorrent"`
	Files []RawFile `json:"files"`
}

// TaskView 是给前端的统一任务视图：实时字段来自 aria2，元数据来自 dl_tasks。
type TaskView struct {
	GID       string  `json:"gid"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Status    string  `json:"status"`
	Total     int64   `json:"total"`
	Completed int64   `json:"completed"`
	Speed     int64   `json:"speed"`
	Progress  float64 `json:"progress"`
	Dir       string  `json:"dir"`
	Path      string  `json:"path"`
	Source    string  `json:"source"`
	Error     string  `json:"error"`
	CreatedAt int64   `json:"createdAt"`
	// BT 相关（非 BT 任务为 0 / 空串）
	UploadSpeed int64   `json:"uploadSpeed"`
	Uploaded    int64   `json:"uploaded"`
	Seeders     int     `json:"seeders"`
	Connections int     `json:"connections"`
	InfoHash    string  `json:"infoHash"`
	IsTorrent   bool    `json:"isTorrent"`
	TorrentName string  `json:"torrentName"`
	ShareRatio  float64 `json:"shareRatio"`
	NumPieces   int64   `json:"numPieces"`
	PieceLength int64   `json:"pieceLength"`
	FileCount   int     `json:"fileCount"`
}

// ListActive 是进度快路径：只问活跃任务一次，等待/已结束由调用方低频补齐。
func (c *Client) ListActive() ([]Status, error) {
	raw, err := c.Call("aria2.tellActive", statusKeys)
	if err != nil {
		return nil, err
	}
	var out []Status
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 aria2.tellActive 结果失败: %w", err)
	}
	return out, nil
}

// ListRest 拉等待 + 已结束（不含活跃），供低频全量刷新用。
func (c *Client) ListRest() ([]Status, error) {
	out := []Status{}
	for _, call := range []struct {
		method string
		args   []any
	}{
		{"aria2.tellWaiting", []any{0, 500, statusKeys}},
		{"aria2.tellStopped", []any{0, 500, statusKeys}},
	} {
		raw, err := c.Call(call.method, call.args...)
		if err != nil {
			return nil, err
		}
		var part []Status
		if err := json.Unmarshal(raw, &part); err != nil {
			return nil, fmt.Errorf("解析 %s 结果失败: %w", call.method, err)
		}
		out = append(out, part...)
	}
	return out, nil
}

// ListStatuses 一次拉回全部活跃 / 等待 / 已停止任务（全量路径）。
func (c *Client) ListStatuses() ([]Status, error) {
	active, err := c.ListActive()
	if err != nil {
		return nil, err
	}
	rest, err := c.ListRest()
	if err != nil {
		return nil, err
	}
	return append(active, rest...), nil
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// ToView 把 aria2 协议对象转成前端可直接渲染的视图。
func (s Status) ToView() TaskView {
	v := TaskView{
		GID:         s.GID,
		Status:      s.Status,
		Total:       parseInt64(s.TotalLength),
		Completed:   parseInt64(s.CompletedLength),
		Speed:       parseInt64(s.DownloadSpeed),
		UploadSpeed: parseInt64(s.UploadSpeed),
		Uploaded:    parseInt64(s.UploadLength),
		Seeders:     int(parseInt64(s.NumSeeders)),
		Connections: int(parseInt64(s.Connections)),
		InfoHash:    s.InfoHash,
		NumPieces:   parseInt64(s.NumPieces),
		PieceLength: parseInt64(s.PieceLength),
		FileCount:   len(s.Files),
		Dir:         s.Dir,
		Error:       s.ErrorMessage,
	}
	if v.Total > 0 {
		v.Progress = float64(v.Completed) * 100 / float64(v.Total)
		if v.Progress > 100 {
			v.Progress = 100
		}
		v.ShareRatio = float64(v.Uploaded) / float64(v.Total)
	}
	// 非 BT 任务有时也会带回一个空的 bittorrent 对象，只有拿到种子名/模式才算真 BT。
	if s.Bittorrent != nil && (s.Bittorrent.Info.Name != "" || s.Bittorrent.Mode != "") {
		v.IsTorrent = true
		v.TorrentName = s.Bittorrent.Info.Name
	}
	if len(s.Files) > 0 {
		v.Path = s.Files[0].Path
		v.Name = filepath.Base(s.Files[0].Path)
		for _, u := range s.Files[0].URIs {
			if u.URI != "" {
				v.URL = u.URI
				break
			}
		}
	}
	if v.TorrentName != "" {
		v.Name = v.TorrentName
	}
	if v.Name == "" || v.Name == "." || v.Name == string(filepath.Separator) {
		if v.URL != "" {
			v.Name = filepath.Base(strings.TrimSuffix(v.URL, "/"))
		}
		if v.Name == "" {
			v.Name = s.GID
		}
	}
	return v
}
