package env

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 默认 Caddyfile：admin 在全局块内（有缩进），站点 :8080 为块头（无缩进）。
const testCaddyfile = "{\n\tadmin localhost:2019\n}\n\n:8080 {\n\trespond \"QuickDock Caddy is running\"\n}\n"

func TestCaddyConfiguredPorts(t *testing.T) {
	tmp := t.TempDir()
	c := &CaddyRuntime{baseDir: tmp}
	ver := "2.8.4"
	if err := os.MkdirAll(filepath.Dir(c.ConfigPath(ver)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.ConfigPath(ver), []byte(testCaddyfile), 0644); err != nil {
		t.Fatal(err)
	}
	// admin 2019 + 站点 8080：两个一块显示
	if got, want := c.ConfiguredPorts(ver), []int{2019, 8080}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredPorts = %v, want %v", got, want)
	}
	// 控制台应打开真实站点端口 8080，而非写死的 80
	if got := c.WebConsolePort(ver); got != 8080 {
		t.Fatalf("WebConsolePort = %d, want 8080", got)
	}
}

// 站点端口改了要跟着变（用户诉求：不再显示写死的默认端口）
func TestCaddyConfiguredPortsCustomSite(t *testing.T) {
	tmp := t.TempDir()
	c := &CaddyRuntime{baseDir: tmp}
	ver := "2.8.4"
	if err := os.MkdirAll(filepath.Dir(c.ConfigPath(ver)), 0755); err != nil {
		t.Fatal(err)
	}
	cf := ":9000 {\n\treverse_proxy localhost:3000\n}\n"
	if err := os.WriteFile(c.ConfigPath(ver), []byte(cf), 0644); err != nil {
		t.Fatal(err)
	}
	// admin 未显式配置 => 回退默认 2019；站点取 :9000；
	// reverse_proxy 的上游 3000 有缩进，不应被当成侦听端口
	if got, want := c.ConfiguredPorts(ver), []int{2019, 9000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredPorts = %v, want %v", got, want)
	}
}

func TestListenPortsInConfNginx(t *testing.T) {
	f := filepath.Join(t.TempDir(), "nginx.conf")
	// 覆盖三种写法：纯端口、host:port、IPv6；127.0.0.1:9000 不能误取成 127
	body := "server {\n    listen       8080;\n    listen       127.0.0.1:9000;\n    listen       [::]:80;\n    server_name  localhost;\n}\n"
	if err := os.WriteFile(f, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := listenPortsInConf(f), []int{8080, 9000, 80}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nginx listen ports = %v, want %v", got, want)
	}
}

func TestListenPortsInConfApache(t *testing.T) {
	f := filepath.Join(t.TempDir(), "httpd.conf")
	if err := os.WriteFile(f, []byte("Listen 80\nListen 127.0.0.1:8080\nListen *:8443\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := listenPortsInConf(f), []int{80, 8080, 8443}; !reflect.DeepEqual(got, want) {
		t.Fatalf("apache listen ports = %v, want %v", got, want)
	}
}

func TestPortsInConfPostgresAndSQL(t *testing.T) {
	tmp := t.TempDir()
	pg := filepath.Join(tmp, "postgresql.conf")
	if err := os.WriteFile(pg, []byte("#port = 5432\nport = 6543\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := portsInConf(pg, rePgPort), []int{6543}; !reflect.DeepEqual(got, want) {
		t.Fatalf("postgres port = %v, want %v", got, want)
	}
	my := filepath.Join(tmp, "my.ini")
	if err := os.WriteFile(my, []byte("[mysqld]\nport=3307\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := portsInConf(my, reSQLPort), []int{3307}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mysql port = %v, want %v", got, want)
	}
}

func TestPortsInConfTraefikAndMongo(t *testing.T) {
	tmp := t.TempDir()
	tf := filepath.Join(tmp, "traefik.yml")
	body := "entryPoints:\n  web:\n    address: \":8080\"\n  websecure:\n    address: \":443\"\n"
	if err := os.WriteFile(tf, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := portsInConf(tf, reTraefikAddr), []int{8080, 443}; !reflect.DeepEqual(got, want) {
		t.Fatalf("traefik ports = %v, want %v", got, want)
	}
	mf := filepath.Join(tmp, "mongod.conf")
	if err := os.WriteFile(mf, []byte("net:\n  port: 27018\n  bindIp: 127.0.0.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := portsInConf(mf, reMongoPort), []int{27018}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mongo port = %v, want %v", got, want)
	}
}

func TestPortsInConfRabbitMQ(t *testing.T) {
	tmp := t.TempDir()
	cf := filepath.Join(tmp, "rabbitmq.conf")
	if err := os.WriteFile(cf, []byte("listeners.tcp.default = 5673\nmanagement.tcp.port = 15673\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, want := portsInConf(cf, reRabbitPort), []int{5673}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rabbit amqp port = %v, want %v", got, want)
	}
	if got, want := portsInConf(cf, reRabbitMgmt), []int{15673}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rabbit mgmt port = %v, want %v", got, want)
	}
}

// 配置文件不存在/解析不到时必须回退默认端口，不能返回空
func TestConfiguredPortsFallback(t *testing.T) {
	if got, want := firstPortOrDefault(nil, 80), []int{80}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %v, want %v", got, want)
	}
	c := &CaddyRuntime{baseDir: t.TempDir()}
	if got, want := c.ConfiguredPorts("nope"), []int{2019, 8080}; !reflect.DeepEqual(got, want) {
		t.Fatalf("caddy fallback = %v, want %v", got, want)
	}
}
