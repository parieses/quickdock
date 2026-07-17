package main

import (
	"encoding/xml"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	wlanapi = syscall.NewLazyDLL("wlanapi.dll")

	procWlanOpenHandle      = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle     = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces  = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanGetProfile      = wlanapi.NewProc("WlanGetProfile")
	procWlanFreeMemory      = wlanapi.NewProc("WlanFreeMemory")
)

// WLAN_PROFILE_GET_PLAINTEXT_KEY = 4
const wlanProfileGetPlaintextKey = 4

type wlanProfileXML struct {
	XMLName xml.Name `xml:"WLANProfile"`
	Name    string   `xml:"name"`
	MSM     struct {
		Security struct {
			SharedKey struct {
				KeyMaterial string `xml:"keyMaterial"`
			} `xml:"sharedKey"`
		} `xml:"security"`
	} `xml:"MSM"`
}

// getWifiPassword 通过 Windows WlanGetProfile API 获取 WiFi 密码（不依赖 netsh）
func getWifiPassword(ssid string) (string, error) {
	// 1. 打开 WLAN 句柄
	var clientHandle uintptr
	var negotiatedVersion uint32
	ret, _, _ := procWlanOpenHandle.Call(
		2, // dwClientVersion (Windows Vista 及以上用 2)
		0, // pReserved
		uintptr(unsafe.Pointer(&negotiatedVersion)), // pdwNegotiatedVersion（第3参数）
		uintptr(unsafe.Pointer(&clientHandle)),      // phClientHandle（第4参数）
	)
	if ret != 0 {
		return "", fmt.Errorf("WlanOpenHandle 失败: 0x%x", ret)
	}
	defer procWlanCloseHandle.Call(clientHandle, 0)

	// 2. 枚举无线网卡接口，获取第一个接口的 GUID
	var ifList *byte
	ret, _, _ = procWlanEnumInterfaces.Call(
		clientHandle,
		0,
		uintptr(unsafe.Pointer(&ifList)),
	)
	if ret != 0 {
		return "", fmt.Errorf("WlanEnumInterfaces 失败: 0x%x", ret)
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(ifList)))

	// WLAN_INTERFACE_INFO_LIST: dwNumberOfItems(4) + dwIndex(4) + InterfaceInfo[0]
	numIfaces := *(*uint32)(unsafe.Pointer(ifList))
	if numIfaces == 0 {
		return "", fmt.Errorf("未找到无线网卡接口")
	}
	// InterfaceInfo 起始偏移 = 4 + 4 = 8；每个 WLAN_INTERFACE_INFO 的 GUID 在前 16 字节
	ifGuid := (*[16]byte)(unsafe.Pointer(uintptr(unsafe.Pointer(ifList)) + 8))

	// 3. 获取配置文件（明文密钥）
	var profileXML *uint16
	var flags uint32 = wlanProfileGetPlaintextKey
	var access uint32

	ret, _, _ = procWlanGetProfile.Call(
		clientHandle,
		uintptr(unsafe.Pointer(ifGuid)),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(ssid))),
		0,        // pReserved
		uintptr(unsafe.Pointer(&profileXML)),
		uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Pointer(&access)),
	)
	if ret != 0 {
		return "", fmt.Errorf("WlanGetProfile 失败 (0x%x) 接口数: %d", ret, numIfaces)
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(profileXML)))

	// 3. 解析 XML
	xmlStr := syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(profileXML))[:])
	var profile wlanProfileXML
	if err := xml.Unmarshal([]byte(xmlStr), &profile); err != nil {
		return "", fmt.Errorf("解析配置文件 XML 失败: %w", err)
	}

	if profile.MSM.Security.SharedKey.KeyMaterial == "" {
		return "", fmt.Errorf("配置文件中未找到密钥")
	}

	return profile.MSM.Security.SharedKey.KeyMaterial, nil
}
