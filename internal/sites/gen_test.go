package sites

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateNginx_Static(t *testing.T) {
	res, err := Generate(GenInput{
		Site:     Site{Name: "主站", Domain: "myapp.test", Dir: `D:\proj\myapp`},
		Backend:  BackendNginx,
		CertPath: `C:\data\sites\certs\sites-cert.pem`,
		KeyPath:  `C:\data\sites\certs\sites-key.pem`,
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if res.FileName != "myapp.test.conf" {
		t.Errorf("文件名不符: %s", res.FileName)
	}
	if res.IncludeLine != "include quickdock-sites/*.conf;" {
		t.Errorf("include 行不符: %s", res.IncludeLine)
	}
	for _, want := range []string{
		"server_name myapp.test;",
		"listen      443 ssl;",
		"ssl_certificate     C:/data/sites/certs/sites-cert.pem;",
		"ssl_certificate_key C:/data/sites/certs/sites-key.pem;",
		"root  D:/proj/myapp;",
		"index index.html index.htm;",
		// 纯静态站点的回退目标是 index.html，绝不是 index.php（那个文件在静态站点上不存在）
		"try_files $uri $uri/ /index.html;",
		"listen      80;",
		"return 301 https://$host$request_uri;",
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
	// 纯静态站点里不该出现任何 PHP 痕迹：FastCGI 段、index.php 回退、index 列表里的 index.php
	if res.NeedsPHPFPM || strings.Contains(res.Snippet, "php") {
		t.Errorf("纯静态站点不应出现任何 PHP 相关指令:\n%s", res.Snippet)
	}
	// 反斜杠在 nginx 里是转义字符，必须全部转成正斜杠
	if strings.Contains(res.Snippet, `\`) {
		t.Errorf("路径未转成正斜杠:\n%s", res.Snippet)
	}
}

func TestGenerateNginx_WithPHP(t *testing.T) {
	res, err := Generate(GenInput{
		Site:       Site{Domain: "php.test", Dir: "/srv/php"},
		Backend:    BackendNginx,
		CertPath:   "/c/cert.pem",
		KeyPath:    "/c/key.pem",
		PHPFPMAddr: "127.0.0.1:9000",
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if !res.NeedsPHPFPM {
		t.Error("应标记需要 PHP-FPM")
	}
	for _, want := range []string{
		`location ~ \.php$ {`,
		"fastcgi_pass   127.0.0.1:9000;",
		"fastcgi_param  SCRIPT_FILENAME $document_root$fastcgi_script_name;",
		// PHP 站点才保留 index.php 作为目录索引与回退目标
		"index index.php index.html index.htm;",
		"try_files $uri $uri/ /index.php?$query_string;",
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
}

// 站点对外端口不可配：无论输入什么，nginx 恒为 443 ssl + 80 的 301 跳转。
func TestGenerateNginx_PortsAreFixed(t *testing.T) {
	res, err := Generate(GenInput{
		Site:    Site{Domain: "x.test", Dir: "/x"},
		Backend: BackendNginx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Snippet, "listen      443 ssl;") {
		t.Errorf("站点本体应固定监听 443 ssl:\n%s", res.Snippet)
	}
	if !strings.Contains(res.Snippet, "listen      80;") {
		t.Errorf("应固定有 80 跳转块:\n%s", res.Snippet)
	}
	// 跳转 URL 不带端口号：本站就是 443，带上反而是错的
	if !strings.Contains(res.Snippet, "return 301 https://$host$request_uri;") {
		t.Errorf("跳转目标应为无端口的 https:\n%s", res.Snippet)
	}
	// 除了这两个端口，不该再出现别的 listen
	if n := strings.Count(res.Snippet, "listen      "); n != 2 {
		t.Errorf("应恰好有 2 条 listen（443/80），实际 %d 条:\n%s", n, res.Snippet)
	}
}

func TestGenerateCaddy(t *testing.T) {
	res, err := Generate(GenInput{
		Site:     Site{Domain: "caddy.test", Dir: `D:\www`},
		Backend:  BackendCaddy,
		CertPath: `D:\certs\c.pem`,
		KeyPath:  `D:\certs\k.pem`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.FileName != "caddy.test.caddy" || res.IncludeLine != "import quickdock-sites/*.caddy" {
		t.Errorf("文件名/include 不符: %s / %s", res.FileName, res.IncludeLine)
	}
	for _, want := range []string{
		"caddy.test {",
		"tls D:/certs/c.pem D:/certs/k.pem",
		"root * D:/www",
		"file_server",
		"try_files {path} {path}/ /index.html",
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
	if strings.Contains(res.Snippet, "php_fastcgi") {
		t.Errorf("未检测到 PHP 时不应生成 php_fastcgi:\n%s", res.Snippet)
	}
}

func TestGenerateCaddy_WithPHPAndHTTPRedirect(t *testing.T) {
	res, err := Generate(GenInput{
		Site:       Site{Domain: "p.test", Dir: "/p"},
		Backend:    BackendCaddy,
		PHPFPMAddr: "127.0.0.1:9000",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 站点本体是不带端口的域名（Caddy 对它有 tls，即 https 443）
	if !strings.Contains(res.Snippet, "p.test {") {
		t.Errorf("站点本体应绑裸域名:\n%s", res.Snippet)
	}
	if !strings.Contains(res.Snippet, "php_fastcgi 127.0.0.1:9000") {
		t.Errorf("缺少 php_fastcgi:\n%s", res.Snippet)
	}
	// 有 PHP 时不应再叠加 spa 回退，否则 .php 请求会被 try_files 先截走
	if strings.Contains(res.Snippet, "try_files") {
		t.Errorf("PHP 站点不应叠加 try_files:\n%s", res.Snippet)
	}
	// 与 nginx 同语义：另给一个 http 块做 301 跳转（Caddy 单块只能绑一个地址）
	if !strings.Contains(res.Snippet, "http://p.test {") {
		t.Errorf("缺少 http 跳转块:\n%s", res.Snippet)
	}
	if !strings.Contains(res.Snippet, "redir https://{host}{uri} 301") {
		t.Errorf("http 块缺少 301 跳转:\n%s", res.Snippet)
	}
}

func TestGenerateValidation(t *testing.T) {
	if _, err := Generate(GenInput{Site: Site{Domain: "a.test", Dir: "/a"}, Backend: "apache"}); err == nil {
		t.Error("未知后端应报错")
	}
	if _, err := Generate(GenInput{Site: Site{Domain: "a.test", Dir: "/a"}, Backend: "builtin"}); err == nil {
		t.Error("内置后端已移除，不应再被接受")
	}
	if _, err := Generate(GenInput{Site: Site{Dir: "/a"}, Backend: BackendNginx}); err == nil {
		t.Error("空域名应报错")
	}
	if _, err := Generate(GenInput{Site: Site{Domain: "a.test"}, Backend: BackendNginx}); err == nil {
		t.Error("空目录应报错")
	}
	if _, err := ParseBackend("nginx"); err != nil {
		t.Errorf("合法后端被拒: %v", err)
	}
}

func TestSuggestedConfDir(t *testing.T) {
	got := SuggestedConfDir(filepath.Join("D:", "rt", "nginx", "1.27", "conf", "nginx.conf"))
	want := filepath.Join("D:", "rt", "nginx", "1.27", "conf", "quickdock-sites")
	if got != want {
		t.Errorf("SuggestedConfDir = %q, want %q", got, want)
	}
	if SuggestedConfDir("") != "" {
		t.Error("空配置路径应返回空目录")
	}
}

func TestNormalizeModules(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"未知与重复被过滤", []string{"cache", "spa", "static", "nope", "cache", "  "}, []string{"spa", "cache"}},
		{"proxy 接管后 spa/static/cache 失效", []string{"cache", "proxy", "static"}, []string{"proxy"}},
		{"spa 优先于 static", []string{"static", "spa"}, []string{"spa"}},
		{"顺序与输入无关", []string{"proxy", "gzip"}, []string{"gzip", "proxy"}},
		{"空输入返回空", nil, nil},
	}
	for _, c := range cases {
		got := NormalizeModules(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: NormalizeModules(%v) = %v, want %v", c.name, c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: NormalizeModules(%v) = %v, want %v", c.name, c.in, got, c.want)
				break
			}
		}
	}
	if len(AllModules()) == 0 {
		t.Error("AllModules 不该为空")
	}
	for _, id := range AllModules() {
		if got := NormalizeModules([]string{id}); len(got) != 1 || got[0] != id {
			t.Errorf("合法模块 %q 被归一化丢弃: %v", id, got)
		}
	}
}

func TestGenerateNginx_Modules(t *testing.T) {
	res, err := Generate(GenInput{
		Site:     Site{Domain: "mod.test", Dir: "/srv/mod"},
		Backend:  BackendNginx,
		CertPath: "/c/c.pem",
		KeyPath:  "/c/k.pem",
		Modules:  []string{"spa", "gzip", "cache", "cors", "security", "hide", "upload"},
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	for _, want := range []string{
		"try_files $uri $uri/ /index.html;",
		"gzip             on;",
		"client_max_body_size 64m;",
		"location ~* \\.(?:js|mjs|css|png|jpe?g|gif|svg|webp|avif|ico|woff2?|ttf|eot|mp4|webm)$ {",
		"add_header Access-Control-Allow-Origin  \"*\" always;",
		"add_header X-Content-Type-Options      nosniff always;",
		"location ~* \\.(git|svn|hg|env|ds_store)(/|$) {",
		"deny all;",
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
	// 勾了 spa 就不该再回退到 index.php（否则静态站点会 404）
	if strings.Contains(res.Snippet, "/index.php?$query_string") {
		t.Errorf("spa 模块下不应保留 index.php 回退:\n%s", res.Snippet)
	}
	// 没勾 proxy 就不该有 proxy_pass
	if strings.Contains(res.Snippet, "proxy_pass") {
		t.Errorf("未勾选反向代理却出现 proxy_pass:\n%s", res.Snippet)
	}
}

func TestGenerateNginx_ProxyTakesOver(t *testing.T) {
	res, err := Generate(GenInput{
		Site:       Site{Domain: "api.test", Dir: "/srv/api"},
		Backend:    BackendNginx,
		Modules:    []string{"proxy", "spa", "cache"},
		ProxyPort:  5173,
		PHPFPMAddr: "127.0.0.1:9000",
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if len(res.Modules) != 1 || res.Modules[0] != "proxy" {
		t.Errorf("代理接管后生效模块应只剩 proxy，实际 %v", res.Modules)
	}
	if res.NeedsPHPFPM {
		t.Error("代理模式下不会生成 FastCGI 段，NeedsPHPFPM 应为 false")
	}
	for _, want := range []string{
		"proxy_pass         http://127.0.0.1:5173;",
		"proxy_set_header   Upgrade           $http_upgrade;",
		"proxy_set_header   Connection        \"upgrade\";",
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
	if strings.Contains(res.Snippet, "fastcgi_pass") {
		t.Errorf("代理模式下不该出现 fastcgi_pass:\n%s", res.Snippet)
	}
	if strings.Contains(res.Snippet, "expires    30d;") {
		t.Errorf("代理模式下不该出现本地静态缓存段:\n%s", res.Snippet)
	}
}

func TestGenerateNginx_ProxyDefaultPort(t *testing.T) {
	res, err := Generate(GenInput{
		Site:    Site{Domain: "d.test", Dir: "/d"},
		Backend: BackendNginx,
		Modules: []string{"proxy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Snippet, "proxy_pass         http://127.0.0.1:3000;") {
		t.Errorf("未指定上游端口时应退回 3000:\n%s", res.Snippet)
	}
}

func TestGenerateNginx_StaticModuleNoFallback(t *testing.T) {
	res, err := Generate(GenInput{
		Site:    Site{Domain: "s.test", Dir: "/s"},
		Backend: BackendNginx,
		Modules: []string{"static"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Snippet, "try_files $uri $uri/ =404;") {
		t.Errorf("纯静态模块应回退成 404:\n%s", res.Snippet)
	}
}

func TestGenerateCaddy_Modules(t *testing.T) {
	res, err := Generate(GenInput{
		Site:     Site{Domain: "cmod.test", Dir: "/srv/cmod"},
		Backend:  BackendCaddy,
		CertPath: "/c/c.pem",
		KeyPath:  "/c/k.pem",
		Modules:  []string{"gzip", "upload", "cors", "security", "hide", "cache"},
	})
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	for _, want := range []string{
		"encode gzip zstd",
		"max_size 64MB",
		"respond @qdcors 204",
		"X-Frame-Options           SAMEORIGIN",
		`@qdhidden path_regexp (?i)\.(git|svn|hg|env|ds_store)(/|$)`,
		`header @qdstatic Cache-Control "public, max-age=2592000, immutable"`,
	} {
		if !strings.Contains(res.Snippet, want) {
			t.Errorf("缺少 %q:\n%s", want, res.Snippet)
		}
	}
}

func TestGenerateCaddy_StaticAndProxy(t *testing.T) {
	res, err := Generate(GenInput{
		Site:      Site{Domain: "cp.test", Dir: "/cp"},
		Backend:   BackendCaddy,
		Modules:   []string{"proxy", "static"},
		ProxyPort: 5173,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Snippet, "reverse_proxy 127.0.0.1:5173") {
		t.Errorf("缺少 reverse_proxy:\n%s", res.Snippet)
	}
	// 代理接管后不该再有本地文件服务与回退
	for _, bad := range []string{"file_server", "try_files", "root *"} {
		if strings.Contains(res.Snippet, bad) {
			t.Errorf("代理模式下不该出现 %q:\n%s", bad, res.Snippet)
		}
	}

	res2, err := Generate(GenInput{
		Site:    Site{Domain: "cs.test", Dir: "/cs"},
		Backend: BackendCaddy,
		Modules: []string{"static"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Snippet, "try_files {path} {path}/") || strings.Contains(res2.Snippet, "/index.html") {
		t.Errorf("纯静态模块不该回退到 index.html:\n%s", res2.Snippet)
	}
}

func TestGenerateDocRoot(t *testing.T) {
	// 框架项目的入口几乎都在子目录（Laravel/ThinkPHP 的 public、Yii2 的 web）。
	// root 不跟着走，.env / storage / vendor 就会落在 web 根下。
	ng, err := Generate(GenInput{
		Site:    Site{Domain: "laravel.test", Dir: `D:\proj\blog`, DocRoot: "public"},
		Backend: BackendNginx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ng.Snippet, "root  D:/proj/blog/public;") {
		t.Errorf("nginx root 未应用文档根:\n%s", ng.Snippet)
	}
	if strings.Contains(ng.Snippet, "root  D:/proj/blog;") {
		t.Errorf("nginx root 仍指向项目根:\n%s", ng.Snippet)
	}

	// 多级文档根（Yii2 advanced 模板的 frontend/web）
	multi, err := Generate(GenInput{
		Site:    Site{Domain: "yii.test", Dir: "/srv/yii", DocRoot: "frontend/web"},
		Backend: BackendNginx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(multi.Snippet, "root  /srv/yii/frontend/web;") {
		t.Errorf("多级文档根未生效:\n%s", multi.Snippet)
	}

	cd, err := Generate(GenInput{
		Site:    Site{Domain: "laravel.test", Dir: "/srv/blog", DocRoot: "public"},
		Backend: BackendCaddy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cd.Snippet, "root * /srv/blog/public") {
		t.Errorf("caddy root 未应用文档根:\n%s", cd.Snippet)
	}

	// 空文档根 = 原行为，已存在的站点不受影响
	plain, err := Generate(GenInput{
		Site:    Site{Domain: "plain.test", Dir: `D:\www`},
		Backend: BackendNginx,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.Snippet, "root  D:/www;") {
		t.Errorf("空文档根时 root 应保持项目目录:\n%s", plain.Snippet)
	}
}
