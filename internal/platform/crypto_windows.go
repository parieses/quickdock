//go:build windows

package platform

import (
	"encoding/base64"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiBlob 对应 Windows DATA_BLOB 结构
type dpapiBlob struct {
	cbData uint32
	pbData *byte
}

func blobFromBytes(b []byte) *dpapiBlob {
	if len(b) == 0 {
		return &dpapiBlob{}
	}
	return &dpapiBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func bytesFromBlob(b *dpapiBlob) []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	return unsafe.Slice(b.pbData, int(b.cbData))
}

var (
	modCrypt32    = windows.NewLazySystemDLL("crypt32.dll")
	procProtect   = modCrypt32.NewProc("CryptProtectData")
	procUnprot    = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree = modKernel32.NewProc("LocalFree")
)

// EncryptSecret 使用 Windows DPAPI 加密敏感字符串（如 API Key），返回 base64 编码。
// 加密与当前用户会话绑定，其他账户/机器无法解密。
func EncryptSecret(plain string) (string, error) {
	in := blobFromBytes([]byte(plain))
	var out dpapiBlob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", fmt.Errorf("DPAPI 加密失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(bytesFromBlob(&out)), nil
}

// DecryptSecret 解密 DPAPI 加密的 base64 字符串
func DecryptSecret(cipher string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil {
		return "", err
	}
	in := blobFromBytes(raw)
	var out dpapiBlob
	r, _, err := procUnprot.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", fmt.Errorf("DPAPI 解密失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return string(bytesFromBlob(&out)), nil
}
