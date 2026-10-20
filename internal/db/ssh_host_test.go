package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// testSshPassword 仅用于单元测试的占位字符串，绝不是真实凭据。
// 使用明显非机密、低熵的占位值，避免被密钥扫描器（GitGuardian 等）误报为「通用密码」。
const testSshPassword = "placeholder-test-credential-no-secret"

// rawSshHostPassword 直接读密文列，用于验证「库里没有明文」。
func rawSshHostPassword(t *testing.T, d *Database, id string) string {
	t.Helper()
	var enc string
	if err := d.conn.QueryRow(`SELECT password FROM ssh_hosts WHERE id = ?`, id).Scan(&enc); err != nil {
		t.Fatalf("读取密文失败: %v", err)
	}
	return enc
}

func TestSshHostCreateEncryptsPassword(t *testing.T) {
	d := openTestDB(t)

	h, err := d.SaveSshHost(SshHost{
		Name: "测试机", Host: "10.0.0.5", Port: 2222, User: "root",
		AuthType: SshAuthPassword, Password: testSshPassword, Env: SshEnvTest,
	})
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if h.ID == "" || h.CreatedAt == "" {
		t.Fatal("应生成 ID 与创建时间")
	}
	if !h.HasPassword {
		t.Fatal("应标记已保存密码")
	}

	// 库里必须是密文。
	enc := rawSshHostPassword(t, d, h.ID)
	if enc == "" {
		t.Fatal("密文列不应为空")
	}
	if enc == testSshPassword || strings.Contains(enc, testSshPassword) {
		t.Fatalf("密码以明文/可识别形式落库: %q", enc)
	}

	// 读取接口绝不带出明文。
	got, err := d.GetSshHost(h.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Password != "" || got.KeyPassphrase != "" {
		t.Fatal("读取接口不得返回明文凭据")
	}
	if !got.HasPassword {
		t.Fatal("读取接口应回 hasPassword 标志")
	}

	// 只有连接前解密入口能拿到明文。
	sec, err := d.GetSshHostSecret(h.ID)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if sec.Password != testSshPassword || sec.Port != 2222 || sec.AuthType != SshAuthPassword {
		t.Fatalf("解密结果不对: %+v", sec)
	}
}

func TestSshHostUpdateKeepsPasswordWhenBlank(t *testing.T) {
	d := openTestDB(t)
	h, err := d.SaveSshHost(SshHost{Host: "10.0.0.5", User: "root", Password: "secret"})
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}

	// 前端编辑表单密码框留空 = 不修改。
	h.Name = "改名了"
	h.Password = ""
	upd, err := d.SaveSshHost(*h)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if !upd.HasPassword {
		t.Fatal("密码留空不应清掉已存密码")
	}
	sec, _ := d.GetSshHostSecret(h.ID)
	if sec.Password != "secret" {
		t.Fatalf("密码应保持不变，实际 %q", sec.Password)
	}

	// 显式清除。
	upd.ClearPassword = true
	if _, err := d.SaveSshHost(*upd); err != nil {
		t.Fatalf("清除密码失败: %v", err)
	}
	if got, _ := d.GetSshHost(h.ID); got.HasPassword {
		t.Fatal("ClearPassword 后应标记为无密码")
	}
	if rawSshHostPassword(t, d, h.ID) != "" {
		t.Fatal("ClearPassword 后密文列应为空")
	}
}

func TestSshHostUpdateExistingPassword(t *testing.T) {
	d := openTestDB(t)
	h, _ := d.SaveSshHost(SshHost{Host: "h", User: "root", Password: "old"})

	h.Password = "new"
	if _, err := d.SaveSshHost(*h); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	sec, _ := d.GetSshHostSecret(h.ID)
	if sec.Password != "new" {
		t.Fatalf("密码应被替换，实际 %q", sec.Password)
	}
}

func TestSshHostKeyAuthPassphrase(t *testing.T) {
	d := openTestDB(t)
	h, err := d.SaveSshHost(SshHost{
		Host: "h", User: "root", AuthType: SshAuthKey,
		KeyPath: "~/.ssh/id_ed25519", KeyPassphrase: "pp", LegacyAlgorithms: true,
	})
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if !h.HasPassphrase || h.HasPassword {
		t.Fatalf("标志位不对: hasPass=%v hasPassphrase=%v", h.HasPassword, h.HasPassphrase)
	}
	sec, _ := d.GetSshHostSecret(h.ID)
	if sec.KeyPassphrase != "pp" || !sec.LegacyAlgorithms || sec.AuthType != SshAuthKey {
		t.Fatalf("凭据不对: %+v", sec)
	}
}

func TestSshHostDefaultsAndNormalize(t *testing.T) {
	d := openTestDB(t)
	h, err := d.SaveSshHost(SshHost{Name: "  ", Host: "  Example.COM  ", User: " root "})
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if h.Name != "Example.COM" {
		t.Fatalf("名称留空应以主机地址兜底，实际 %q", h.Name)
	}
	if h.Host != "Example.COM" || h.User != "root" {
		t.Fatalf("应去掉首尾空格，实际 host=%q user=%q", h.Host, h.User)
	}
	if h.Port != 22 || h.AuthType != SshAuthPassword || h.Env != SshEnvTest {
		t.Fatalf("默认值不对: port=%d auth=%q env=%q", h.Port, h.AuthType, h.Env)
	}
}

func TestSshHostRequiresAddress(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.SaveSshHost(SshHost{Name: "x"}); err == nil {
		t.Fatal("非文件夹缺少主机地址应报错")
	}
	// 文件夹不需要地址。
	if _, err := d.SaveSshHost(SshHost{Name: "生产环境", IsFolder: true}); err != nil {
		t.Fatalf("文件夹不应要求主机地址: %v", err)
	}
}

func TestSshFolderTree(t *testing.T) {
	d := openTestDB(t)
	folder, err := d.SaveSshHost(SshHost{Name: "阿里云", IsFolder: true})
	if err != nil {
		t.Fatalf("建文件夹失败: %v", err)
	}
	child, err := d.SaveSshHost(SshHost{Name: "prod-01", Host: "1.1.1.1", ParentID: folder.ID})
	if err != nil {
		t.Fatalf("建子节点失败: %v", err)
	}
	if child.Sort == 0 {
		t.Fatal("新建子节点应自动分配 sort")
	}
	child2, _ := d.SaveSshHost(SshHost{Name: "prod-02", Host: "1.1.1.2", ParentID: folder.ID})
	if child2.Sort <= child.Sort {
		t.Fatalf("同父节点下 sort 应递增，实际 %d → %d", child.Sort, child2.Sort)
	}

	list, err := d.ListSshHosts()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 个节点，实际 %d", len(list))
	}

	// 移动。
	if err := d.MoveSshHost(child2.ID, ""); err != nil {
		t.Fatalf("移动失败: %v", err)
	}
	moved, _ := d.GetSshHost(child2.ID)
	if moved.ParentID != "" {
		t.Fatalf("移动后 parent_id 应为空，实际 %q", moved.ParentID)
	}

	// 删文件夹应带走整棵子树。
	if err := d.DeleteSshHost(folder.ID); err != nil {
		t.Fatalf("删除文件夹失败: %v", err)
	}
	if _, err := d.GetSshHost(child.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("子节点应随文件夹一起删除，实际: %v", err)
	}
}

func TestSshHostRejectsInvalidParent(t *testing.T) {
	d := openTestDB(t)
	host, _ := d.SaveSshHost(SshHost{Host: "1.1.1.1"})
	folder, _ := d.SaveSshHost(SshHost{Name: "f", IsFolder: true})
	sub, _ := d.SaveSshHost(SshHost{Name: "sub", IsFolder: true, ParentID: folder.ID})

	// 父节点不存在。
	if _, err := d.SaveSshHost(SshHost{Host: "2.2.2.2", ParentID: "nope"}); err == nil {
		t.Fatal("父节点不存在应报错")
	}
	// 父节点是主机而不是文件夹。
	if _, err := d.SaveSshHost(SshHost{Host: "2.2.2.2", ParentID: host.ID}); err == nil {
		t.Fatal("父节点不是文件夹应报错")
	}
	// 挂到自己下面。
	if _, err := d.SaveSshHost(SshHost{ID: folder.ID, Name: "f", IsFolder: true, ParentID: folder.ID}); err == nil {
		t.Fatal("挂到自身应报错")
	}
	// 形成环：文件夹挂到自己的子文件夹下。
	if err := d.MoveSshHost(folder.ID, sub.ID); err == nil {
		t.Fatal("把文件夹移到自己的子文件夹下应报错")
	}
}

func TestSshHostDeleteMissing(t *testing.T) {
	d := openTestDB(t)
	if err := d.DeleteSshHost("ghost"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除不存在的节点应返回 ErrNoRows，实际: %v", err)
	}
}

func TestSshHostTouchConnected(t *testing.T) {
	d := openTestDB(t)
	h, _ := d.SaveSshHost(SshHost{Host: "1.1.1.1"})
	if h.LastConnectedAt != "" {
		t.Fatal("新建时 last_connected_at 应为空")
	}
	if err := d.TouchSshHostConnected(h.ID); err != nil {
		t.Fatalf("更新时间失败: %v", err)
	}
	got, _ := d.GetSshHost(h.ID)
	if got.LastConnectedAt == "" {
		t.Fatal("连接后应记录时间")
	}
}

// ---- 已信任主机密钥 ----

func TestSshKnownHostCRUD(t *testing.T) {
	d := openTestDB(t)

	if err := d.SaveSshKnownHost(SshKnownHost{
		Host: "1.2.3.4", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:AAA",
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 同类型再存 = 覆盖，不产生第二条。
	if err := d.SaveSshKnownHost(SshKnownHost{
		Host: "1.2.3.4", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:BBB", AddedAt: 12345,
	}); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	// 不同类型是另一条记录（同一台机器可能同时有 ed25519 与 rsa）。
	if err := d.SaveSshKnownHost(SshKnownHost{
		Host: "1.2.3.4", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:CCC",
	}); err != nil {
		t.Fatalf("保存第二种类型失败: %v", err)
	}

	list, err := d.ListSshKnownHosts("1.2.3.4", 22)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应有 2 条（两种密钥类型），实际 %d", len(list))
	}
	for _, k := range list {
		if k.KeyType == "ssh-ed25519" && k.Fingerprint != "SHA256:BBB" {
			t.Fatalf("同类型应被覆盖，实际 %q", k.Fingerprint)
		}
	}

	// 主机名规范化：大小写、空格、括号都要落到同一条记录上。
	if list, err := d.ListSshKnownHosts("  1.2.3.4  ", 22); err != nil || len(list) != 2 {
		t.Fatalf("规范化后应查到同两条，err=%v n=%d", err, len(list))
	}
	if err := d.SaveSshKnownHost(SshKnownHost{Host: "Other.Host", KeyType: "ssh-ed25519", Fingerprint: "SHA256:DDD"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if list, _ := d.ListSshKnownHosts("other.host", 22); len(list) != 1 {
		t.Fatalf("主机名应忽略大小写，实际 %d 条", len(list))
	}

	if err := d.DeleteSshKnownHosts("1.2.3.4", 22); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if list, _ := d.ListSshKnownHosts("1.2.3.4", 22); len(list) != 0 {
		t.Fatalf("删除后应为空，实际 %d 条", len(list))
	}
	// 只删该主机，不影响其它记录。
	if list, _ := d.ListSshKnownHosts("other.host", 22); len(list) != 1 {
		t.Fatal("不应误删其它主机记录")
	}
}

func TestSshKnownHostRejectsEmptyKey(t *testing.T) {
	d := openTestDB(t)
	if err := d.SaveSshKnownHost(SshKnownHost{Host: "h", Port: 22, Fingerprint: "SHA256:X"}); err == nil {
		t.Fatal("缺少密钥类型应报错")
	}
	if err := d.SaveSshKnownHost(SshKnownHost{Host: "h", Port: 22, KeyType: "ssh-ed25519"}); err == nil {
		t.Fatal("缺少指纹应报错")
	}
}

// TestSshSchemaReopen 验证建表幂等：老库直接打开不报错，数据还在。
func TestSshSchemaReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	h, err := d1.SaveSshHost(SshHost{Name: "keep", Host: "1.1.1.1", Password: "pw"})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败（建表不幂等？）: %v", err)
	}
	defer d2.Close()

	got, err := d2.GetSshHost(h.ID)
	if err != nil {
		t.Fatalf("重开后读取失败: %v", err)
	}
	if got.Name != "keep" || !got.HasPassword {
		t.Fatalf("重开后数据不对: %+v", got)
	}
	sec, _ := d2.GetSshHostSecret(h.ID)
	if sec.Password != "pw" {
		t.Fatal("重开后密文应仍可解密")
	}
}
