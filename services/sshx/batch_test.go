package sshx

import (
	"context"
	"strings"
	"testing"
	"time"

	"quickdock/internal/db"
)

// ---- 预演（SshBatchPlan）----

func TestSshBatchPlanSkipsUnusableHosts(t *testing.T) {
	svc := newTestService(t)

	r1 := svc.SaveHost(SshHostInput{
		Name: "web-1", Host: "10.0.0.1", Port: 22, User: "root",
		AuthType: "password", Password: "pw", Env: "prod",
	})
	r2 := svc.SaveHost(SshHostInput{
		Name: "web-2", Host: "10.0.0.2", Port: 22, User: "root",
		AuthType: "password", Password: "pw", Env: "test",
	})
	// 文件夹：批量执行的目标必须是具体主机。
	rf := svc.SaveHost(SshHostInput{Name: "分组", IsFolder: true})
	// 没有凭据：能被选中，但连不上，必须在预演阶段就标出来。
	rn := svc.SaveHost(SshHostInput{Name: "nopw", Host: "10.0.0.3", Port: 22, User: "root"})

	folderID := rf.Data.(*db.SshHost).ID
	nopwID := rn.Data.(*db.SshHost).ID

	ids := []string{
		r1.Data.(*db.SshHost).ID,
		r2.Data.(*db.SshHost).ID,
		folderID,
		nopwID,
		"ghost", // 不存在
	}

	plan := svc.SshBatchPlan(SshBatchPlanRequest{HostIDs: ids, Command: "uptime"}).Data.(SshBatchPlan)
	if len(plan.Hosts) != 2 {
		t.Fatalf("应有 2 台可执行主机，实际 %d: %+v", len(plan.Hosts), plan.Hosts)
	}
	if plan.ProdCount != 1 {
		t.Fatalf("生产机数量应为 1，实际 %d", plan.ProdCount)
	}
	if len(plan.Skipped) != 3 {
		t.Fatalf("应跳过 3 项（文件夹/无凭据/不存在），实际 %d: %+v", len(plan.Skipped), plan.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range plan.Skipped {
		reasons[s.HostID] = s.Reason
	}
	if reasons[folderID] != CodeFolderNotConnectable {
		t.Fatalf("文件夹跳过原因应为 %s，实际 %q", CodeFolderNotConnectable, reasons[folderID])
	}
	if reasons[nopwID] != CodeNoCredential {
		t.Fatalf("无凭据跳过原因应为 %s，实际 %q", CodeNoCredential, reasons[nopwID])
	}
	if reasons["ghost"] != CodeHostNotFound {
		t.Fatalf("不存在的 id 应为 %s，实际 %q", CodeHostNotFound, reasons["ghost"])
	}
	// 地址串要能直接认人，否则结果表里全是「主机 1/2/3」。
	if plan.Hosts[0].Addr != "root@10.0.0.1:22" {
		t.Fatalf("地址格式不对: %q", plan.Hosts[0].Addr)
	}
}

func TestSshBatchPlanDedupesHostIDs(t *testing.T) {
	svc := newTestService(t)
	id := svc.SaveHost(SshHostInput{
		Name: "web", Host: "10.0.0.1", Port: 22, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost).ID

	plan := svc.SshBatchPlan(SshBatchPlanRequest{
		HostIDs: []string{id, id, " " + id + " "},
		Command: "uptime",
	}).Data.(SshBatchPlan)

	// 同一台主机跑两遍写操作，后果不必细说。
	if len(plan.Hosts) != 1 {
		t.Fatalf("重复 id 应去重成 1 台，实际 %d", len(plan.Hosts))
	}
}

func TestSshBatchPlanRiskFromBackend(t *testing.T) {
	svc := newTestService(t)
	id := svc.SaveHost(SshHostInput{
		Name: "web", Host: "10.0.0.1", Port: 22, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost).ID

	high := svc.SshBatchPlan(SshBatchPlanRequest{HostIDs: []string{id}, Command: "rm -rf /data"}).Data.(SshBatchPlan)
	if high.RiskLevel != "high" {
		t.Fatalf("rm -rf 应判为 high，实际 %q", high.RiskLevel)
	}
	if strings.TrimSpace(high.RiskNote) == "" {
		t.Fatal("高风险必须带说明，否则界面上只有一个红框没有理由")
	}

	low := svc.SshBatchPlan(SshBatchPlanRequest{HostIDs: []string{id}, Command: "uptime"}).Data.(SshBatchPlan)
	if low.RiskLevel != "" {
		t.Fatalf("uptime 不该被判风险，实际 %q", low.RiskLevel)
	}
}

// ---- 执行 ----

func TestSshBatchRunRejectsBadInput(t *testing.T) {
	svc := newTestService(t)
	id := svc.SaveHost(SshHostInput{
		Name: "web", Host: "10.0.0.1", Port: 22, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost).ID

	if r := svc.SshBatchRun(SshBatchRequest{HostIDs: []string{id}, Command: "  "}); errorCode(r) != CodeEmptyCommand {
		t.Fatalf("空命令应返回 %s，实际 %+v", CodeEmptyCommand, r)
	}
	if r := svc.SshBatchRun(SshBatchRequest{HostIDs: []string{"ghost"}, Command: "uptime"}); r.Code != 1 {
		t.Fatalf("没有可执行主机时应失败，实际 %+v", r)
	}
}

// trustTestServer 建一台指向本地测试服务端的主机并完成指纹信任。
//
// 顺序不能颠倒：批量执行不会自动信任未知指纹（与终端/SFTP 一致），
// 未信任时结果行的错误码是 CodeHostKeyUnknown，命令一条都跑不了。
func trustTestServer(t *testing.T, svc *SshService, name string) *db.SshHost {
	t.Helper()
	host, port := startSvcTestServer(t)
	h := svc.SaveHost(SshHostInput{
		Name: name, Host: host, Port: port, User: "root",
		AuthType: "password", Password: "pw", Env: "test",
	}).Data.(*db.SshHost)

	// 第一次连接只记录「待确认指纹」，不做判断。
	res := svc.TestHost(h.ID)
	if res.Code != 0 {
		t.Fatalf("首连不应失败: %+v", res)
	}
	fp := res.Data.(TestHostResult).Fingerprint
	if r := svc.TrustFingerprint(h.Host, h.Port, fp); r.Code != 0 {
		t.Fatalf("信任指纹失败: %+v", r)
	}
	return h
}

func TestExecOnBatchHostResults(t *testing.T) {
	svc := newTestService(t)
	good := trustTestServer(t, svc, "good")

	// 127.0.0.1:1 立即被拒，不用等 15s 超时。
	dead := svc.SaveHost(SshHostInput{
		Name: "dead", Host: "127.0.0.1", Port: 1, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost)

	target := SshBatchTarget{HostID: good.ID, Name: good.Name, Addr: "root@x", Env: good.Env}
	res := svc.execOnBatchHost(context.Background(), "b1", target, "echo hi", 5*time.Second)
	if res.ErrorCode != "" {
		t.Fatalf("应执行成功，实际错误码 %s: %s", res.ErrorCode, res.Error)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码应为 0，实际 %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hi") {
		t.Fatalf("应拿到 echo 输出，实际 %q", res.Stdout)
	}
	if res.BatchID != "b1" || res.HostID != good.ID {
		t.Fatalf("结果必须带批次与主机身份: %+v", res)
	}

	deadRes := svc.execOnBatchHost(context.Background(), "b1",
		SshBatchTarget{HostID: dead.ID, Name: dead.Name}, "uptime", 3*time.Second)
	if deadRes.ErrorCode != CodeUnreachable {
		t.Fatalf("不可达主机应为 %s，实际 %q（%s）", CodeUnreachable, deadRes.ErrorCode, deadRes.Error)
	}
	// 没跑完的命令不该有退出码，否则结果表会把它算进「命令失败」。
	if deadRes.ExitCode != 0 {
		t.Fatalf("未执行的主机退出码应为 0，实际 %d", deadRes.ExitCode)
	}
}

func TestExecOnBatchHostCanceledContext(t *testing.T) {
	svc := newTestService(t)
	id := svc.SaveHost(SshHostInput{
		Name: "web", Host: "10.0.0.1", Port: 22, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost).ID

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := svc.execOnBatchHost(ctx, "b1", SshBatchTarget{HostID: id}, "uptime", time.Second)
	// 关键：不能沿用 classifySshError 对 context.Canceled 的归类
	// （那是给 SFTP 传输用的），批量里必须说「批次已取消」。
	if res.ErrorCode != CodeBatchCanceled {
		t.Fatalf("取消应为 %s，实际 %q", CodeBatchCanceled, res.ErrorCode)
	}
}

func TestSshBatchCancelUnknownIsOK(t *testing.T) {
	svc := newTestService(t)
	// 批次可能刚好自然结束，「取消一个不存在的批次」不该报错。
	if r := svc.SshBatchCancel("nope"); r.Code != 0 {
		t.Fatalf("取消未知批次应成功，实际 %+v", r)
	}
	if r := svc.SshBatchCancel("  "); errorCode(r) != CodeInvalidInput {
		t.Fatalf("空 id 应返回 %s，实际 %+v", CodeInvalidInput, r)
	}
}

// ---- 输出裁剪 ----

func TestClipBatchOutput(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := clipBatchOutput(short); got != short {
		t.Fatal("未超上限的内容不该被改动")
	}

	long := strings.Repeat("a", batchHostCap+10)
	got := clipBatchOutput(long)
	if len(got) <= batchHostCap {
		t.Fatal("超上限应保留上限内容再加标记")
	}
	if !strings.Contains(got, "已截断") {
		t.Fatal("裁剪必须留下可见标记，否则用户以为命令只输出了这么多")
	}

	// 多字节字符被切在中间会变成替换符：必须回退到合法边界。
	cn := strings.Repeat("中", batchHostCap) // 每个 3 字节，远超上限
	trunc := clipBatchOutput(cn)
	body := strings.SplitN(trunc, "\n…", 2)[0]
	if !strings.HasPrefix(cn, body) {
		t.Fatal("裁剪点应落在 UTF-8 字符边界上")
	}
	if strings.Contains(body, "\uFFFD") {
		t.Fatal("裁剪不得产生替换符")
	}
}
