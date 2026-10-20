package plugin

import (
	"bytes"
	"strings"
	"testing"
)

// 加密/解密往返：同一口令 + 同一 salt 必须能解回原文。
func TestCryptoRoundTrip(t *testing.T) {
	salt := []byte("0123456789abcdef")
	plain := []byte("要加密的密码库内容 {\"entries\":[{\"site\":\"a\"}]}")

	iv, data, err := EncryptForPlugin("主密码123", salt, plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if iv == "" || data == "" {
		t.Fatal("iv/data 不应为空")
	}

	got, err := DecryptForPlugin("主密码123", salt, iv, data)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("解密结果不一致:\n got %q\nwant %q", got, plain)
	}
}

// 错误口令必须解不开，且错误不能是空（否则插件无法向用户提示「密码错误」）。
func TestCryptoWrongPassword(t *testing.T) {
	salt := []byte("0123456789abcdef")
	iv, data, err := EncryptForPlugin("right", salt, []byte("secret"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if _, err := DecryptForPlugin("wrong", salt, iv, data); err == nil {
		t.Fatal("错误口令应解密失败")
	}
}

// 密文被改一个字节就必须解不开——这是 GCM 认证标签的职责，
// 也是它与「异或混淆」的本质区别。
func TestCryptoTamperDetected(t *testing.T) {
	salt := []byte("0123456789abcdef")
	iv, data, err := EncryptForPlugin("pw", salt, []byte("secret"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	raw, err := B64(data)
	if err != nil {
		t.Fatalf("base64 解码失败: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if _, err := DecryptForPlugin("pw", salt, iv, B64Enc(raw)); err == nil {
		t.Fatal("密文被篡改后应解密失败（GCM 认证标签）")
	}
}

// 相同口令 + 相同 salt，两次加密的 IV 必须不同：
// IV 复用会让 GCM 的安全性大幅下降（同一密钥下重复 IV 可恢复认证密钥）。
func TestCryptoRandomIV(t *testing.T) {
	salt := []byte("0123456789abcdef")
	iv1, d1, err := EncryptForPlugin("pw", salt, []byte("same"))
	if err != nil {
		t.Fatalf("第一次加密失败: %v", err)
	}
	iv2, d2, err := EncryptForPlugin("pw", salt, []byte("same"))
	if err != nil {
		t.Fatalf("第二次加密失败: %v", err)
	}
	if iv1 == iv2 {
		t.Error("两次加密的 IV 相同——IV 必须是随机的")
	}
	if d1 == d2 {
		t.Error("两次加密的密文相同——IV 不同时日文密文也应不同")
	}
	// 但两者都应能各自解回原文
	for i, pair := range [][2]string{{iv1, d1}, {iv2, d2}} {
		got, err := DecryptForPlugin("pw", salt, pair[0], pair[1])
		if err != nil {
			t.Fatalf("第 %d 组解密失败: %v", i+1, err)
		}
		if string(got) != "same" {
			t.Errorf("第 %d 组解密结果不对: %q", i+1, got)
		}
	}
}

// 明文上限：超限必须拒绝，且错误要能识别（插件据此提示用户）。
func TestCryptoPlainTooLarge(t *testing.T) {
	salt := []byte("0123456789abcdef")
	big := make([]byte, pluginCryptoMaxPlain+1)
	_, _, err := EncryptForPlugin("pw", salt, big)
	if err == nil {
		t.Fatal("超过上限的明文应被拒绝")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Errorf("错误信息应说明上限，实际: %v", err)
	}
}

func TestCryptoEmptySalt(t *testing.T) {
	if _, _, err := EncryptForPlugin("pw", nil, []byte("x")); err == nil {
		t.Fatal("空 salt 应被拒绝")
	}
	if _, err := DecryptForPlugin("pw", nil, "iv", "data"); err == nil {
		t.Fatal("空 salt 应被拒绝")
	}
}

// IV 长度不对时必须在调 gcm.Open 之前就拦住，
// 否则会得到一个语义模糊的「解密失败」而查不到真实原因。
func TestCryptoBadIVLength(t *testing.T) {
	salt := []byte("0123456789abcdef")
	iv, data, err := EncryptForPlugin("pw", salt, []byte("x"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	_, err = DecryptForPlugin("pw", salt, B64Enc([]byte("short")), data)
	if err == nil {
		t.Fatal("IV 长度不符应失败")
	}
	if !strings.Contains(err.Error(), "IV 长度") {
		t.Errorf("错误信息应点明 IV 长度，实际: %v", err)
	}
	// 原 IV 仍应能解开，证明上面失败不是别的原因导致的
	if _, err := DecryptForPlugin("pw", salt, iv, data); err != nil {
		t.Fatalf("原 IV 应能解开: %v", err)
	}
}

// base64 编解码：标准与 URL-safe 两种变体都要能解，
// 插件手写其中任一种都不该被拒。
func TestCryptoB64(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []byte
	}{
		{"标准 base64 带 padding", "aGVsbG8=", []byte("hello")},
		// URL-safe 变体：含 '-' 与 '_'，标准 base64 解不了，必须走 URL 分支。
		// 该串解码后是字节 0xfb 0xff 0xbf 0xff（用 Python 校验过）。
		{"URL-safe 字符", "-_-__w==", []byte{0xfb, 0xff, 0xbf, 0xff}},
		{"空串返回空", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := B64(c.in)
			if err != nil {
				t.Fatalf("解码失败: %v", err)
			}
			if !bytes.Equal(got, c.want) {
				t.Errorf("B64(%q) = %v, 期望 %v", c.in, got, c.want)
			}
		})
	}
	if _, err := B64("!!!不是 base64!!!"); err == nil {
		t.Error("非法 base64 应报错")
	}
}

// 随机数：长度必须精确，且两次结果不同（否则 salt/IV 会重复）。
func TestCryptoRandom(t *testing.T) {
	for _, n := range []int{1, 16, 32, 64} {
		s, err := RandomForPlugin(n)
		if err != nil {
			t.Fatalf("n=%d 失败: %v", n, err)
		}
		raw, err := B64(s)
		if err != nil {
			t.Fatalf("n=%d 结果不是合法 base64: %v", n, err)
		}
		if len(raw) != n {
			t.Errorf("n=%d 得到 %d 字节", n, len(raw))
		}
	}
	a, _ := RandomForPlugin(16)
	b, _ := RandomForPlugin(16)
	if a == b {
		t.Error("两次随机结果相同")
	}
	// 边界：0 与超大值必须拒绝
	if _, err := RandomForPlugin(0); err == nil {
		t.Error("0 字节应被拒绝")
	}
	if _, err := RandomForPlugin(4097); err == nil {
		t.Error("超过上限应被拒绝")
	}
}
