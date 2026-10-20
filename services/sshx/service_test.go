package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

func newTestService(t *testing.T) *SshService {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "ssh.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	svc := NewSshService(&services.AppService{DB: d})
	// 每个测试实例都带一个 Batcher goroutine，必须收掉，否则测试进程里越积越多。
	t.Cleanup(svc.Shutdown)
	return svc
}

// startSvcTestServer 起一个最小 SSH 服务端：密码认证 + 支持 exec echo。
func startSvcTestServer(t *testing.T) (host string, port int) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成服务端密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造 signer 失败: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					go serveSvcChannel(newCh)
				}
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func serveSvcChannel(newCh ssh.NewChannel) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" || len(req.Payload) < 4 {
			req.Reply(false, nil)
			continue
		}
		n := binary.BigEndian.Uint32(req.Payload[:4])
		if int(n) > len(req.Payload)-4 {
			req.Reply(false, nil)
			continue
		}
		cmd := string(req.Payload[4 : 4+n])
		req.Reply(true, nil)
		if strings.HasPrefix(cmd, "echo ") {
			ch.Write([]byte(strings.TrimPrefix(cmd, "echo ") + "\n"))
		}
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload, 0)
		ch.SendRequest("exit-status", false, payload)
		return
	}
}

// ---- 主机管理 ----

func TestSaveHostAndListKeepsSecretsOut(t *testing.T) {
	svc := newTestService(t)

	r := svc.SaveHost(SshHostInput{
		Name: "测试机", Host: "127.0.0.1", Port: 1, User: "root",
		AuthType: "password", Password: "topsecret", Env: "test",
	})
	if r.Code != 0 {
		t.Fatalf("保存失败: %+v", r)
	}
	saved := r.Data.(*db.SshHost)
	if !saved.HasPassword {
		t.Fatal("应标记已保存密码")
	}

	list := svc.ListHosts()
	if list.Code != 0 {
		t.Fatalf("列表失败: %+v", list)
	}
	hosts := list.Data.([]db.SshHost)
	if len(hosts) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(hosts))
	}
	// 列表返回的结构体里 password 字段必须是空的（db 层根本不查该列）。
	if hosts[0].Password != "" || hosts[0].KeyPassphrase != "" {
		t.Fatal("列表不得带出明文凭据")
	}
	if !hosts[0].HasPassword {
		t.Fatal("应通过 hasPassword 告知前端是否已保存")
	}

	// 「测试连接」对一个没人监听的端口会失败，错误码要稳定（不靠文案）。
	// 用 127.0.0.1:1 而不是公网不可达地址：连接被立即拒绝，测试不用等 15s 超时。
	r = svc.TestHost(saved.ID)
	if r.Code != 1 || errorCode(r) != CodeUnreachable {
		t.Fatalf("不可达主机应返回 unreachable: %+v", r)
	}
}

func TestTestHostInvalidTargets(t *testing.T) {
	svc := newTestService(t)

	// ID 为空。
	if r := svc.TestHost("  "); r.Code != 1 || errorCode(r) != CodeInvalidInput {
		t.Fatalf("空 ID 应返回 invalid_input，实际 %+v", r)
	}
	// 不存在。
	if r := svc.TestHost("ghost"); r.Code != 1 || errorCode(r) != CodeHostNotFound {
		t.Fatalf("不存在的 ID 应返回 host_not_found，实际 %+v", r)
	}

	// 文件夹不能连接。
	rf := svc.SaveHost(SshHostInput{Name: "生产环境", IsFolder: true})
	folder := rf.Data.(*db.SshHost)
	if r := svc.TestHost(folder.ID); errorCode(r) != CodeFolderNotConnectable {
		t.Fatalf("文件夹应返回 folder_not_connectable，实际 %+v", r)
	}

	// 没有凭据。
	rh := svc.SaveHost(SshHostInput{Name: "nopw", Host: "127.0.0.1", Port: 1, User: "root"})
	host := rh.Data.(*db.SshHost)
	if r := svc.TestHost(host.ID); errorCode(r) != CodeNoCredential {
		t.Fatalf("无凭据应返回 no_credential，实际 %+v", r)
	}
}

// ---- 连通 → 信任 → 再连通 → 执行 ----

func TestTestHostTrustFlow(t *testing.T) {
	host, port := startSvcTestServer(t)
	svc := newTestService(t)

	r := svc.SaveHost(SshHostInput{
		Name: "本地测试", Host: host, Port: port, User: "root", Password: "pw",
	})
	if r.Code != 0 {
		t.Fatalf("保存失败: %+v", r)
	}
	h := r.Data.(*db.SshHost)

	// 第一次：连得上但主机密钥未信任。
	res := svc.TestHost(h.ID)
	if res.Code != 0 {
		t.Fatalf("首连不应直接失败（应回待确认指纹），实际: %+v", res)
	}
	tr := res.Data.(TestHostResult)
	if !tr.NeedsTrust || tr.Trusted {
		t.Fatalf("应处于待确认状态: %+v", tr)
	}
	if !strings.HasPrefix(tr.Fingerprint, "SHA256:") {
		t.Fatalf("指纹格式不对: %q", tr.Fingerprint)
	}

	// 指纹不匹配必须拒绝（防前端传任意值把未知密钥写进白名单）。
	if r := svc.TrustFingerprint(h.Host, h.Port, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); errorCode(r) != CodeFingerprintMismatch {
		t.Fatalf("错误指纹应被拒绝，实际 %+v", r)
	}

	// 正确信任。
	if r := svc.TrustFingerprint(h.Host, h.Port, tr.Fingerprint); r.Code != 0 {
		t.Fatalf("信任失败: %+v", r)
	}

	// 第二次：应直接连通并带回服务端版本。
	res = svc.TestHost(h.ID)
	if res.Code != 0 {
		t.Fatalf("信任后应连通，实际 %+v", res)
	}
	tr = res.Data.(TestHostResult)
	if !tr.Trusted || tr.NeedsTrust {
		t.Fatalf("应为已信任状态: %+v", tr)
	}
	if tr.ServerVersion == "" || tr.HostKeyAlgorithm == "" {
		t.Fatalf("应带回握手信息: %+v", tr)
	}
	if tr.LatencyMs < 0 {
		t.Fatalf("耗时应非负: %d", tr.LatencyMs)
	}

	// 连接时间应被记录。
	if got, err := svc.App.DB.GetSshHost(h.ID); err != nil || got.LastConnectedAt == "" {
		t.Fatalf("应记录最近连接时间, err=%v at=%q", err, got.LastConnectedAt)
	}

	// 复用同一套凭据执行命令。
	er := svc.Exec(h.ID, "echo hi", 10)
	if er.Code != 0 {
		t.Fatalf("执行失败: %+v", er)
	}
	out := er.Data.(ExecResult)
	if out.Stdout != "hi\n" || out.ExitCode != 0 {
		t.Fatalf("执行结果不对: %+v", out)
	}
}

func TestExecValidation(t *testing.T) {
	svc := newTestService(t)
	if r := svc.Exec("", "echo hi", 5); errorCode(r) != CodeInvalidInput {
		t.Fatalf("空主机应返回 invalid_input，实际 %+v", r)
	}

	r := svc.SaveHost(SshHostInput{Name: "x", Host: "127.0.0.1", Port: 1, User: "root", Password: "pw"})
	h := r.Data.(*db.SshHost)
	if r := svc.Exec(h.ID, "   ", 5); errorCode(r) != CodeEmptyCommand {
		t.Fatalf("空命令应返回 empty_command，实际 %+v", r)
	}
}

// ---- 指纹存储适配 ----

func TestKnownHostStorePersists(t *testing.T) {
	svc := newTestService(t)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("转换公钥失败: %v", err)
	}
	fp := ssh.FingerprintSHA256(key)

	// 首次校验：未命中，留下 pending。
	if err := svc.known.Verify("10.1.1.1", 22, key); !errors.Is(err, sshcore.ErrUnknownHostKey) {
		t.Fatalf("应返回未信任，实际: %v", err)
	}

	// 经门面信任 → 走适配器落库。
	if r := svc.TrustFingerprint("10.1.1.1", 22, fp); r.Code != 0 {
		t.Fatalf("信任失败: %+v", r)
	}

	// 数据库里应有记录。
	recs, err := svc.App.DB.ListSshKnownHosts("10.1.1.1", 22)
	if err != nil || len(recs) != 1 {
		t.Fatalf("应落库 1 条，err=%v n=%d", err, len(recs))
	}
	if recs[0].Fingerprint != fp || recs[0].KeyType != key.Type() {
		t.Fatalf("落库内容不对: %+v", recs[0])
	}

	// 换一个全新的服务实例（新的内存 pending 表）仍应放行 —— 证明是真落库而非内存态。
	fresh := NewSshService(svc.App)
	if err := fresh.known.Verify("10.1.1.1", 22, key); err != nil {
		t.Fatalf("新实例应从库里读到信任记录，实际: %v", err)
	}

	// 列表接口。
	lr := svc.ListTrustedKeys("10.1.1.1", 22)
	if lr.Code != 0 {
		t.Fatalf("列表失败: %+v", lr)
	}
	if list := lr.Data.([]sshcore.KnownHost); len(list) != 1 || list[0].Fingerprint != fp {
		t.Fatalf("列表内容不对: %+v", list)
	}

	// 清除后回到待确认。
	if r := svc.RemoveTrustedKeys("10.1.1.1", 22); r.Code != 0 {
		t.Fatalf("清除失败: %+v", r)
	}
	if err := fresh.known.Verify("10.1.1.1", 22, key); !errors.Is(err, sshcore.ErrUnknownHostKey) {
		t.Fatalf("清除后应重新要求确认，实际: %v", err)
	}
}

// ---- 错误码映射 ----

func TestClassifySshError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"认证失败", fmt.Errorf("%w: bad password", sshcore.ErrAuthFailed), CodeAuthFailed},
		{"网络不可达", fmt.Errorf("%w: dial tcp", sshcore.ErrUnreachable), CodeUnreachable},
		{"算法不兼容", sshcore.ErrAlgorithmGone, CodeAlgorithmUnsupported},
		{"无可用认证", sshcore.ErrNoAuthMethod, CodeNoAuthMethod},
		{"连接已关闭", sshcore.ErrClosed, CodeConnectionClosed},
		{"命令超时", fmt.Errorf("%w: context deadline", sshcore.ErrExecTimeout), CodeExecTimeout},
		{"命令中断", sshcore.ErrExecAborted, CodeExecAborted},
		{"空命令", sshcore.ErrEmptyCommand, CodeEmptyCommand},
		{"无待确认密钥", sshcore.ErrNoPendingHostKey, CodeNoPendingKey},
		{"指纹不匹配", sshcore.ErrFingerprintMismatch, CodeFingerprintMismatch},
		{"主机不存在", fmt.Errorf("读取失败: %w", sql.ErrNoRows), CodeHostNotFound},
		{"数据库未就绪", errDBUnavailable, CodeDBUnavailable},
		{"文件夹", errFolderNotConnectable, CodeFolderNotConnectable},
		{"网络错误兜底", &net.OpError{Op: "dial", Err: errors.New("refused")}, CodeUnreachable},
		{"未知错误", errors.New("something odd"), CodeInternal},
	}
	for _, c := range cases {
		if got, _ := classifySshError(c.err); got != c.want {
			t.Errorf("%s: 期望 %s，实际 %s", c.name, c.want, got)
		}
	}
}

func TestClassifyHostKeyErrorsCarryDetail(t *testing.T) {
	// 指纹两类错误必须优先于 Sentinel 命中，且把新旧指纹带出去——
	// 前端要靠它渲染「确认指纹」和「指纹已变更」两种完全不同的对话框。
	code, detail := classifySshError(&sshcore.UnknownHostKeyError{
		Host: "h", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:NEW",
	})
	if code != CodeHostKeyUnknown {
		t.Fatalf("期望 %s，实际 %s", CodeHostKeyUnknown, code)
	}
	if detail["fingerprint"] != "SHA256:NEW" || detail["keyType"] != "ssh-ed25519" {
		t.Fatalf("缺少指纹详情: %+v", detail)
	}

	code, detail = classifySshError(&sshcore.HostKeyChangedError{
		Host: "h", Port: 22, KeyType: "ssh-ed25519",
		OldFingerprint: "SHA256:OLD", NewFingerprint: "SHA256:NEW",
	})
	if code != CodeHostKeyChanged {
		t.Fatalf("期望 %s，实际 %s", CodeHostKeyChanged, code)
	}
	if detail["oldFingerprint"] != "SHA256:OLD" || detail["newFingerprint"] != "SHA256:NEW" {
		t.Fatalf("缺少新旧指纹: %+v", detail)
	}

	// 经过 fmt.Errorf("%w") 包装后仍要能识别（http 层/上层可能再包一次）。
	wrapped := fmt.Errorf("连接失败: %w", &sshcore.HostKeyChangedError{Host: "h", Port: 22})
	if code, _ := classifySshError(wrapped); code != CodeHostKeyChanged {
		t.Fatalf("包装后应仍识别为指纹变更，实际 %s", code)
	}
}

// TestSaveHostValidationMapsToInvalidInput 守住「用户填错表单」的归类。
//
// db 层的校验错误如果没包 db.ErrSshValidation，就会落到兜底分支变成 ssh_internal，
// 前端显示「内部错误」，用户完全不知道是自己漏填了一个字段。这个 bug 不影响功能，
// 所以只能靠测试发现。
func TestSaveHostValidationMapsToInvalidInput(t *testing.T) {
	svc := newTestService(t)

	// 主机地址为空。
	r := svc.SaveHost(SshHostInput{Name: "无名", AuthType: "password", Password: "pw"})
	if got := errorCode(r); got != CodeInvalidInput {
		t.Fatalf("空地址期望 %s，实际 %s（msg=%s）", CodeInvalidInput, got, r.Msg)
	}
	if !strings.Contains(r.Msg, "主机地址") {
		t.Fatalf("错误信息应保留具体原因，实际: %s", r.Msg)
	}

	// 文件夹名字为空。
	if got := errorCode(svc.SaveHost(SshHostInput{IsFolder: true})); got != CodeInvalidInput {
		t.Fatalf("空文件夹名期望 %s，实际 %s", CodeInvalidInput, got)
	}

	// 挂到一个不是文件夹的节点下。
	hostID := svc.SaveHost(SshHostInput{
		Name: "机", Host: "10.0.0.1", Port: 22, User: "root", Password: "pw",
	}).Data.(*db.SshHost).ID
	if got := errorCode(svc.SaveHost(SshHostInput{
		Name: "子", Host: "10.0.0.2", ParentID: hostID,
	})); got != CodeInvalidInput {
		t.Fatalf("父节点非文件夹期望 %s，实际 %s", CodeInvalidInput, got)
	}

	// 文件夹移到自己的子树下（成环）。
	parentID := svc.SaveHost(SshHostInput{Name: "父目录", IsFolder: true}).Data.(*db.SshHost).ID
	childID := svc.SaveHost(SshHostInput{Name: "子目录", IsFolder: true, ParentID: parentID}).Data.(*db.SshHost).ID
	if got := errorCode(svc.MoveHost(parentID, childID)); got != CodeInvalidInput {
		t.Fatalf("移成环期望 %s，实际 %s", CodeInvalidInput, got)
	}
}

// TestExecOnUntrustedHostSurfacesFingerprint 守住「未信任 ≠ 连接失败」这条语义。
//
// TestHost 对未信任主机返回 Ok + needsTrust，但 Exec 走的是同一条拨号链路，
// 只能在返回值里带 ssh_hostkey_unknown。前端如果不认这个码，就会把
// 「请先确认指纹」渲染成一条红色错误，用户重试多少次都连不上。
func TestExecOnUntrustedHostSurfacesFingerprint(t *testing.T) {
	svc := newTestService(t)
	host, port := startSvcTestServer(t)

	h := svc.SaveHost(SshHostInput{
		Name: "srv", Host: host, Port: port, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost)

	res := svc.Exec(h.ID, "echo hi", 5)
	if res.Code != 1 {
		t.Fatalf("未信任主机应先失败并等用户确认: %+v", res)
	}
	if got := errorCode(res); got != CodeHostKeyUnknown {
		t.Fatalf("期望 %s，实际 %s", CodeHostKeyUnknown, got)
	}
	data := res.Data.(map[string]interface{})
	fp, _ := data["fingerprint"].(string)
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("应带回指纹供前端弹窗: %+v", data)
	}

	// 信任后重试：证明 Exec 的拨号与 TestHost 共用同一个 pending/存储链路。
	if tr := svc.TrustFingerprint(host, port, fp); tr.Code != 0 {
		t.Fatalf("信任失败: %+v", tr)
	}
	res = svc.Exec(h.ID, "echo hi", 5)
	if res.Code != 0 {
		t.Fatalf("信任后应成功: %+v", res)
	}
	if out := res.Data.(ExecResult); out.Stdout != "hi\n" || out.ExitCode != 0 {
		t.Fatalf("输出不对: %+v", out)
	}
}

// TestSshHostInputCoversWritableFields 守住「入参结构体与实体表同步」。
//
// 只用 SshHostInput 接收前端写入。db.SshHost 以后加了可写字段（比如 proxyJump），
// 而 Input 没跟着加，编译不会报错，表现是「表单里能填，保存后消失」——
// 这类字段漂移只能靠反射检查。
func TestSshHostInputCoversWritableFields(t *testing.T) {
	// 只读字段：后端生成或由查询推导，前端不该写。
	readOnly := map[string]bool{
		"HasPassword":     true,
		"HasPassphrase":   true,
		"LastConnectedAt": true,
		"CreatedAt":       true,
		"UpdatedAt":       true,
	}

	inType := reflect.TypeOf(SshHostInput{})
	dbType := reflect.TypeOf(db.SshHost{})
	for i := 0; i < dbType.NumField(); i++ {
		f := dbType.Field(i)
		if readOnly[f.Name] {
			continue
		}
		inf, ok := inType.FieldByName(f.Name)
		if !ok {
			t.Errorf("db.SshHost.%s 在 SshHostInput 里缺失，前端将无法写入该字段", f.Name)
			continue
		}
		if inf.Type != f.Type {
			t.Errorf("字段 %s 类型不一致: input=%s db=%s", f.Name, inf.Type, f.Type)
		}
	}
}

// TestKnownHostStructParity 守住 store.go 里手写映射的两端结构同构。
//
// 适配器的转换是逐字段写的：名字写错会编译失败，但「只在一边新增字段」不会——
// 那会静默丢数据（比如指纹表加了备注列，引擎侧永远读不到）。双向检查挡住这种漂移。
func TestKnownHostStructParity(t *testing.T) {
	dbType := reflect.TypeOf(db.SshKnownHost{})
	engType := reflect.TypeOf(sshcore.KnownHost{})

	for i := 0; i < dbType.NumField(); i++ {
		f := dbType.Field(i)
		eng, ok := engType.FieldByName(f.Name)
		if !ok {
			t.Errorf("db.SshKnownHost.%s 在 sshx.KnownHost 里不存在，映射时会丢失", f.Name)
			continue
		}
		if eng.Type != f.Type {
			t.Errorf("%s 类型不一致: db=%s engine=%s", f.Name, f.Type, eng.Type)
		}
	}
	for i := 0; i < engType.NumField(); i++ {
		f := engType.Field(i)
		if _, ok := dbType.FieldByName(f.Name); !ok {
			t.Errorf("sshx.KnownHost.%s 在 db.SshKnownHost 里不存在，无法落库", f.Name)
		}
	}
}

// errorCode 从失败结果里取 errorCode。
func errorCode(r *services.ApiResult) string {
	if r == nil {
		return ""
	}
	m, ok := r.Data.(map[string]interface{})
	if !ok {
		return ""
	}
	s, _ := m["errorCode"].(string)
	return s
}

// TestListAllTrustedKeys 管理页要一次拿到全部已信任记录，
// 否则前端得按主机逐台查（那是 N 次 IPC）。
func TestListAllTrustedKeys(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)

	// 先信任一条（走 TestHost 拿到指纹再 Trust，与真实流程一致）。
	tr := svc.TestHost(saveTrustedHost(t, svc, host, port).ID)
	// saveTrustedHost 里已经信任过了，这里再取一次指纹验证列表能查到。
	res, ok := tr.Data.(TestHostResult)
	if ok && res.Fingerprint != "" {
		svc.TrustFingerprint(host, port, res.Fingerprint)
	}

	all := svc.ListAllTrustedKeys()
	if all.Code != 0 {
		t.Fatalf("列出全部指纹失败: %+v", all)
	}
	list := all.Data.([]TrustedHost)
	var found *TrustedHost
	for i := range list {
		if list[i].Host == host && list[i].Port == port {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("列表里找不到 %s:%d 的记录，实际 %d 条", host, port, len(list))
	}
	if found.Display == "" || found.KeyType == "" || found.Fingerprint == "" {
		t.Fatalf("记录字段不全: %+v", found)
	}
	// hostId/hostName 是给用户辨认归属用的：能对上就该填上。
	if found.HostID == "" {
		t.Fatalf("应能关联到已保存的主机 ID，实际为空（前端会因此显示不出归属）")
	}
}

// TestRemoveTrustedKeys 管理页的撤销入口。
//
// 这是当前唯一能让用户摆脱「旧指纹一直挡住连接」的办法 ——
// 服务器重装或换密钥后，用户除了清数据库没有任何正常路径。
func TestRemoveTrustedKeys(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	before := svc.ListAllTrustedKeys().Data.([]TrustedHost)
	if len(before) == 0 {
		t.Fatal("前置条件不成立：应有已信任记录")
	}

	if r := svc.RemoveTrustedKeys(host, port); r.Code != 0 {
		t.Fatalf("清除指纹失败: %+v", r)
	}

	after := svc.ListAllTrustedKeys().Data.([]TrustedHost)
	if len(after) >= len(before) {
		t.Fatalf("清除后记录数应减少，实际 %d -> %d", len(before), len(after))
	}
	for _, a := range after {
		if a.Host == host && a.Port == port {
			t.Fatalf("记录未被清除: %+v", a)
		}
	}

	// 关键验证：清掉之后连接会重新走「待确认」而不是被旧指纹拦住。
	// 这才是用户真正想要的——换密钥后能重新信任。
	r := svc.Connect(h.ID, 80, 24)
	if r.Code == 0 {
		t.Fatal("清掉指纹后不应能直接连上，应停在待确认")
	}
	if code := errorCode(r); code != CodeHostKeyUnknown {
		t.Fatalf("应返回「主机密钥未知」，实际 %s", code)
	}
}
