package aria2

import (
	"strings"
	"testing"
)

// 本包的测试刻意不碰真实文件系统与端口：LoadConfig 会扫端口、写 rpc.json，
// 在测试进程里跑会污染用户数据目录。只覆盖纯配置推导与启动参数拼装。

func TestSpeedLimitArg(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "0"},
		{"0", "0"},
		{" 0 ", "0"},
		{"100", "100K"},
		{" 512 ", "512K"},
		{"2M", "2M"},
		{"128K", "128K"},
	}
	for _, c := range cases {
		if got := SpeedLimitArg(c.in); got != c.want {
			t.Errorf("SpeedLimitArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 老 rpc.json 里只有最早那几个字段，反序列化后新增的 bool 全是零值 false。
// 反向命名（DisableXxx）就是为了让零值等价于「BT 功能全开」；这个测试锁住该约定。
func TestApplyDefaultsKeepsLegacyConfigWorking(t *testing.T) {
	c := &Config{Port: 16800, Secret: "s", DownloadDir: "D:/dl"}
	c.applyDefaults()
	if c.MaxConcurrent != 5 || c.Split != 8 || c.MaxConnPerSrv != 8 {
		t.Fatalf("并发默认值不对: concurrent=%d split=%d perServer=%d", c.MaxConcurrent, c.Split, c.MaxConnPerSrv)
	}
	if c.SeedRatio != "0.0" || c.SeedTime != "0" {
		t.Fatalf("做种默认值不对: ratio=%q time=%q", c.SeedRatio, c.SeedTime)
	}
	if len(c.CategoryRules) == 0 {
		t.Fatal("分类规则未被补齐")
	}
	if c.RemotePort != 6800 || c.RemotePath != "/jsonrpc" {
		t.Fatalf("远程默认值不对: port=%d path=%q", c.RemotePort, c.RemotePath)
	}
	if c.IsRemote() {
		t.Fatal("未开 RemoteEnabled 时 IsRemote 应为 false")
	}

	args := strings.Join(DaemonArgs(c), "\n")
	for _, want := range []string{
		"--enable-dht=true", "--enable-dht6=true", "--enable-peer-exchange=true",
		"--bt-save-metadata=true", "--bt-load-saved-metadata=true", "--follow-torrent=true",
		"--bt-remove-unselected-file=false", "--bt-force-encryption=false",
		"--bt-require-crypto=false", "--check-integrity=false",
		"--listen-port=6881-6980", "--dht-listen-port=6881-6980",
		"--seed-ratio=0.0", "--seed-time=0",
		"--rpc-listen-all=false", "--rpc-secret=s", "--continue=true",
		"--console-log-level=warn", "--save-session-interval=60",
		"--dht-file-path=", "--dht-file-path6=",
		"--file-allocation=none", "--dir=D:/dl",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("默认 DaemonArgs 缺 %q", want)
		}
	}
}

func TestDaemonArgsBTToggles(t *testing.T) {
	c := &Config{
		Port: 16800, Secret: "s", DownloadDir: "D:/dl",
		DisableDht: true, DisableDht6: true, DisablePeerExchange: true,
		DisableSaveMetadata: true, DisableFollowTorrent: true,
		BtForceEncryption: true, BtRequireCrypto: true,
		BtRemoveUnselected: true, CheckIntegrity: true,
		ListenPort: 7000, DhtListenPort: 7100,
		SeedRatio: "1.5", SeedTime: "60",
	}
	args := strings.Join(DaemonArgs(c), "\n")
	for _, want := range []string{
		"--enable-dht=false", "--enable-dht6=false", "--enable-peer-exchange=false",
		"--bt-save-metadata=false", "--follow-torrent=false",
		"--bt-remove-unselected-file=true", "--bt-force-encryption=true",
		"--bt-require-crypto=true", "--check-integrity=true",
		"--listen-port=7000-7099", "--dht-listen-port=7100-7199",
		"--seed-ratio=1.5", "--seed-time=60",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("切换后 DaemonArgs 缺 %q", want)
		}
	}
}

func TestDaemonArgsOptionalFlags(t *testing.T) {
	base := func() *Config {
		return &Config{Port: 16800, Secret: "s", DownloadDir: "D:/dl"}
	}
	empty := strings.Join(DaemonArgs(base()), "\n")
	for _, forbidden := range []string{
		"--max-overall-download-limit", "--max-overall-upload-limit",
		"--bt-tracker", "--min-split-size", "--user-agent", "--all-proxy",
	} {
		if strings.Contains(empty, forbidden) {
			t.Errorf("空配置不该带 %q", forbidden)
		}
	}

	c := base()
	c.DownloadLimit, c.UploadLimit = "512", "2M"
	c.BtTracker = "udp://tracker.example:80/announce"
	c.MinSplitSize = " 20M "
	c.UserAgent = "QD/1.0"
	c.Proxy = "http://127.0.0.1:7890"
	filled := strings.Join(DaemonArgs(c), "\n")
	for _, want := range []string{
		"--max-overall-download-limit=512K", "--max-overall-upload-limit=2M",
		"--bt-tracker=udp://tracker.example:80/announce",
		"--min-split-size=20M",
		"--user-agent=QD/1.0",
		"--all-proxy=http://127.0.0.1:7890",
	} {
		if !strings.Contains(filled, want) {
			t.Errorf("可选参数缺失 %q", want)
		}
	}

	// "0" 是前端的「不限速」，不能被拼成一个值为 0 的限速参数。
	c2 := base()
	c2.DownloadLimit, c2.UploadLimit = "0", "0"
	zeroed := strings.Join(DaemonArgs(c2), "\n")
	if strings.Contains(zeroed, "--max-overall-download-limit") {
		t.Error("DownloadLimit=0 不该下发 --max-overall-download-limit")
	}
	if strings.Contains(zeroed, "--max-overall-upload-limit") {
		t.Error("UploadLimit=0 不该下发 --max-overall-upload-limit")
	}
}

func TestRemoteEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		base, ws string
		secret   string
		remote   bool
	}{
		{
			name: "local", cfg: Config{Port: 16800, Secret: "local"},
			base: "http://127.0.0.1:16800/jsonrpc", ws: "ws://127.0.0.1:16800/jsonrpc",
			secret: "local",
		},
		{
			name: "remote plain", cfg: Config{RemoteEnabled: true, RemoteHost: "nas.local", RemoteSecret: "remote", Secret: "local"},
			base: "http://nas.local:6800/jsonrpc", ws: "ws://nas.local:6800/jsonrpc",
			secret: "remote", remote: true,
		},
		{
			name: "remote secure with custom path",
			cfg: Config{RemoteEnabled: true, RemoteHost: "https://dl.example.com/aria2/", RemotePort: 443,
				RemoteSecure: true, RemotePath: "rpc", RemoteSecret: "k"},
			base: "https://dl.example.com:443/rpc", ws: "wss://dl.example.com:443/rpc",
			secret: "k", remote: true,
		},
		{
			name: "host embeds port", cfg: Config{RemoteEnabled: true, RemoteHost: "10.0.0.2:16801"},
			base: "http://10.0.0.2:16801/jsonrpc", ws: "ws://10.0.0.2:16801/jsonrpc",
			secret: "", remote: true,
		},
		{
			name: "ipv6 host", cfg: Config{RemoteEnabled: true, RemoteHost: "[::1]:6800"},
			base: "http://[::1]:6800/jsonrpc", ws: "ws://[::1]:6800/jsonrpc",
			secret: "", remote: true,
		},
		{
			// 开了开关但没填主机：不能让半截配置把本机 daemon 顶掉。
			name: "enabled but empty host", cfg: Config{RemoteEnabled: true, RemoteHost: "  ", Port: 16800, Secret: "local"},
			base: "http://127.0.0.1:16800/jsonrpc", ws: "ws://127.0.0.1:16800/jsonrpc",
			secret: "local",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.cfg
			if got := cfg.IsRemote(); got != c.remote {
				t.Errorf("IsRemote() = %v, want %v", got, c.remote)
			}
			if got := cfg.RPCBase(); got != c.base {
				t.Errorf("RPCBase() = %q, want %q", got, c.base)
			}
			if got := cfg.WSBase(); got != c.ws {
				t.Errorf("WSBase() = %q, want %q", got, c.ws)
			}
			if got := cfg.RPCSecret(); got != c.secret {
				t.Errorf("RPCSecret() = %q, want %q", got, c.secret)
			}
		})
	}
}
