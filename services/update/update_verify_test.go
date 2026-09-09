package update

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// endpointManifest 是 endpoint provider 消费的签名 manifest 的最小结构。
// 字段名必须与 wails v3 endpoint provider 的解析严格一致（见 updater/pkg/updater/providers/endpoint）。
type endpointArtifact struct {
	Platform      string `json:"platform"`
	Arch          string `json:"arch"`
	URL           string `json:"url"`
	Filename      string `json:"filename"`
	Filetype      string `json:"filetype"`
	Size          int64  `json:"size"`
	DigestAlgo    string `json:"digestAlgo"`
	Digest        string `json:"digest"`
	SignatureAlgo string `json:"signatureAlgo"`
	Signature     string `json:"signature"`
}

type endpointManifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	Version       string             `json:"version"`
	Artifacts     []endpointArtifact `json:"artifacts"`
}

// signArtifact 复刻 wails3 updater sign 的密码学：
// digest = SHA512(file)；sig = ed25519.Sign(priv, digest, Hash=SHA512)。
// 框架两端都对 digest 再做一次 SHA512（即 SHA512(SHA512(file))），
// 这里必须与之一致 —— 验证器 runVerification 用同样方式验签。
func signArtifact(priv ed25519.PrivateKey, artifact []byte) (digest, sig []byte) {
	d := sha512.Sum512(artifact)
	s, err := priv.Sign(rand.Reader, d[:], &ed25519.Options{Hash: crypto.SHA512})
	if err != nil {
		panic(err)
	}
	return d[:], s
}

// newSignedServer 用 priv/pub 对 artifact 签名，起本地服务，返回 endpoint provider 与 Check 结果。
// serveBytes 是实际下发的字节（可与被签名的 artifact 不同，用于模拟篡改）。
func newSignedServer(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, serveBytes []byte) (*endpoint.Provider, *updater.Release) {
	t.Helper()
	signedDigest, signedSig := signArtifact(priv, serveBytes)

	man := endpointManifest{
		SchemaVersion: 1,
		Version:       "9.9.9", // 比 0.0.0 新且非预发布，避免 semver 判定无更新
		Artifacts: []endpointArtifact{{
			Platform:      "windows",
			Arch:          "amd64",
			URL:           "",
			Filename:      "quickdock-amd64.exe",
			Filetype:      "exe",
			Size:          int64(len(serveBytes)),
			DigestAlgo:    "sha512",
			Digest:        base64.StdEncoding.EncodeToString(signedDigest),
			SignatureAlgo: "ed25519ph",
			Signature:     base64.StdEncoding.EncodeToString(signedSig),
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(man)
		case "/quickdock-amd64.exe":
			_, _ = w.Write(serveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	man.Artifacts[0].URL = srv.URL + "/quickdock-amd64.exe" // handler 闭包请求时读取，无需重建 server
	t.Cleanup(srv.Close)

	ep, err := endpoint.New(endpoint.Config{URL: srv.URL + "/manifest.json"})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ep.Check(context.Background(), updater.CheckRequest{
		Platform:       "windows",
		Arch:           "amd64",
		CurrentVersion: "0.0.0",
	})
	if err != nil {
		t.Fatalf("endpoint Check failed: %v", err)
	}
	if rel == nil {
		t.Fatal("endpoint Check returned nil (no update detected)")
	}
	_ = pub
	return ep, rel
}

// TestUpdaterSignatureVerify_LocalE2E 正例：签名 manifest -> endpoint Check
// 填出 Verification -> 用与 runVerification 完全相同的 ed25519ph 密码学验签通过；
// 下载字节的 sha512 也与 manifest 摘要一致。
func TestUpdaterSignatureVerify_LocalE2E(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("QuickDock update payload v0.2.1 -- fake binary for local e2e")

	ep, rel := newSignedServer(t, priv, pub, artifact)

	if rel.Verification == nil {
		t.Fatal("Verification block not populated from signed manifest")
	}
	if rel.Verification.SignatureAlgo != "ed25519ph" {
		t.Fatalf("SignatureAlgo = %q, want ed25519ph", rel.Verification.SignatureAlgo)
	}
	if len(rel.Verification.Signature) != ed25519.SignatureSize {
		t.Fatalf("Signature len = %d, want %d", len(rel.Verification.Signature), ed25519.SignatureSize)
	}

	// 与 runVerification 完全一致的验签
	dl := downloadArtifact(t, ep, rel)
	d := sha512.Sum512(dl)
	if err := ed25519.VerifyWithOptions(pub, d[:], rel.Verification.Signature, &ed25519.Options{Hash: crypto.SHA512}); err != nil {
		t.Fatalf("signature did NOT verify against public key (should pass): %v", err)
	}
	if string(dl) != string(artifact) {
		t.Fatal("downloaded bytes mismatch")
	}
	t.Log("signed manifest -> endpoint Check -> Verification populated -> ed25519ph verify + digest match: OK")
}

// TestUpdaterSignatureVerify_TamperDetected 反例（fail closed）：
// manifest 签名的是 good，但 server 下发被篡改的 bad 字节 —— 验签必须失败。
func TestUpdaterSignatureVerify_TamperDetected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	good := []byte("legit payload")
	bad := []byte("attacker replaced this")

	// 用 good 签名，但下发 bad（newSignedServer 内部会按 serveBytes=bad 重新签名 bad，
	// 为了让"签名的是 good、下发的是 bad"，这里手动构造：签名 good，服务下发 bad）
	signedDigest, signedSig := signArtifact(priv, good)
	man := endpointManifest{
		SchemaVersion: 1,
		Version:       "9.9.9",
		Artifacts: []endpointArtifact{{
			Platform:      "windows",
			Arch:          "amd64",
			URL:           "",
			Filename:      "quickdock-amd64.exe",
			Filetype:      "exe",
			Size:          int64(len(bad)),
			DigestAlgo:    "sha512",
			Digest:        base64.StdEncoding.EncodeToString(signedDigest),
			SignatureAlgo: "ed25519ph",
			Signature:     base64.StdEncoding.EncodeToString(signedSig),
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(man)
		case "/quickdock-amd64.exe":
			_, _ = w.Write(bad) // 故意下发被篡改的内容
		default:
			http.NotFound(w, r)
		}
	}))
	man.Artifacts[0].URL = srv.URL + "/quickdock-amd64.exe"
	defer srv.Close()

	ep, _ := endpoint.New(endpoint.Config{URL: srv.URL + "/manifest.json"})
	rel, err := ep.Check(context.Background(), updater.CheckRequest{Platform: "windows", Arch: "amd64", CurrentVersion: "0.0.0"})
	if err != nil || rel == nil || rel.Verification == nil {
		t.Fatalf("Check/precondition failed: rel=%v err=%v", rel, err)
	}

	dl := downloadArtifact(t, ep, rel)
	dlDigest := sha512.Sum512(dl)
	if string(dlDigest[:]) == string(signedDigest) {
		t.Fatal("tampered artifact digest unexpectedly matched signed digest (test broken)")
	}
	// 真实 updater 的 runVerification 会因 digest mismatch 直接报错（fail closed）；
	// 再确认签名本身也验不过（双重保险）。
	if err := ed25519.VerifyWithOptions(pub, dlDigest[:], rel.Verification.Signature, &ed25519.Options{Hash: crypto.SHA512}); err == nil {
		t.Fatal("tampered artifact signature unexpectedly verified (MUST fail closed)")
	}
	t.Log("tamper detected: digest mismatch + signature rejects forged bytes -> fail closed OK")
}

func downloadArtifact(t *testing.T, p updater.Provider, rel *updater.Release) []byte {
	t.Helper()
	var sink writeCounter
	if err := p.Download(context.Background(), rel, &sink, nil); err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	return sink.data
}

// TestUpdaterMirrorWrapperE2E 验证真实 app 路径：endpoint provider 被
// NewMirrorUpdaterProvider 包装后（与 main.go initUpdater 完全一致），
// Check/Download 仍正确委托，签名 Verification 正常填充、下载字节可验签。
func TestUpdaterMirrorWrapperE2E(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("QuickDock payload via mirror-wrapped endpoint provider")

	inner, _ := newSignedServer(t, priv, pub, artifact)
	wrapped := NewMirrorUpdaterProvider(inner, &http.Client{Timeout: 0})

	// Check 经包装器委托给 endpoint
	rel, err := wrapped.Check(context.Background(), updater.CheckRequest{
		Platform:       "windows",
		Arch:           "amd64",
		CurrentVersion: "0.0.0",
	})
	if err != nil {
		t.Fatalf("wrapped Check failed: %v", err)
	}
	if rel == nil {
		t.Fatal("wrapped Check returned nil")
	}
	if rel.Verification == nil || rel.Verification.SignatureAlgo != "ed25519ph" {
		t.Fatal("wrapped Check did not populate signed Verification")
	}

	// Download 经包装器（读 endpoint.artifact.url metadata，直连 + 镜像重试）
	dl := downloadArtifact(t, wrapped, rel)
	d := sha512.Sum512(dl)
	if err := ed25519.VerifyWithOptions(pub, d[:], rel.Verification.Signature, &ed25519.Options{Hash: crypto.SHA512}); err != nil {
		t.Fatalf("wrapped download signature verify failed: %v", err)
	}
	if string(dl) != string(artifact) {
		t.Fatal("wrapped download bytes mismatch")
	}
	t.Log("mirror-wrapped endpoint: Check + Download + ed25519ph verify OK")
}

type writeCounter struct{ data []byte }

func (w *writeCounter) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}
