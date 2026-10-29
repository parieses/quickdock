package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// TestMirrorProviderCheckFallback 验证"检查更新"路径的镜像回退：
// 直连 provider 的 Check 失败时，mirrorProvider 应改用镜像前缀重拉 manifest，
// 并正常返回带签名 Verification 的 Release（国内直连 github.com 间歇超时的兜底）。
func TestMirrorProviderCheckFallback(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("QuickDock payload served via mirror check fallback")
	digest, sig := signArtifact(priv, artifact)

	man := endpointManifest{
		SchemaVersion: 1,
		Version:       "9.9.9",
		Artifacts: []endpointArtifact{{
			Platform:      "windows",
			Arch:          "amd64",
			Filename:      "quickdock-amd64.exe",
			Filetype:      "exe",
			Size:          int64(len(artifact)),
			DigestAlgo:    "sha512",
			Digest:        base64.StdEncoding.EncodeToString(digest),
			SignatureAlgo: "ed25519ph",
			Signature:     base64.StdEncoding.EncodeToString(sig),
		}},
	}
	// 镜像服务器：任意路径都回 manifest（真实镜像按原样转发 GitHub 路径），
	// /payload 回 artifact 字节。manifest 在 server 起来后才能填绝对 artifact URL，
	// handler 闭包读取 man 变量，事后赋值即可。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/payload" {
			_, _ = w.Write(artifact)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(man)
	}))
	defer srv.Close()
	man.Artifacts[0].URL = srv.URL + "/payload"

	// inner 指向一个必然连接失败的地址，模拟直连 GitHub 被掐断
	inner, _ := endpoint.New(endpoint.Config{URL: "http://127.0.0.1:1/manifest.json"})
	wrapped := NewMirrorUpdaterProvider(inner, &http.Client{}, ManifestURL, srv.URL+"/")

	rel, err := wrapped.Check(context.Background(), updater.CheckRequest{
		Platform:       "windows",
		Arch:           "amd64",
		CurrentVersion: "0.0.0",
	})
	if err != nil {
		t.Fatalf("镜像回退 Check 失败: %v", err)
	}
	if rel == nil || rel.Version != "9.9.9" {
		t.Fatalf("镜像回退返回异常: rel=%v", rel)
	}
	if rel.Verification == nil || rel.Verification.SignatureAlgo != "ed25519ph" {
		t.Fatal("镜像回退的 Release 未携带签名 Verification")
	}
	// 镜像命中的 artifact URL 必须是 manifest 里的【绝对】GitHub 地址
	// （本测试中即镜像服务的 /payload），不应被镜像前缀二次拼接
	if rel.Metadata["endpoint.artifact.url"] != srv.URL+"/payload" {
		t.Fatalf("artifact URL 解析异常: %v", rel.Metadata["endpoint.artifact.url"])
	}
}
