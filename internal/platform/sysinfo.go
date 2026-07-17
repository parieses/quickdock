package platform

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)



// SystemStatus 系统资源概览
type SystemStatus struct {
	CPUPercent    float64    `json:"cpuPercent"`
	MemUsedGB     float64    `json:"memUsedGB"`
	MemTotalGB    float64    `json:"memTotalGB"`
	MemPercent    float64    `json:"memPercent"`
	Disks         []DiskInfo `json:"disks"`
	IPs           []string   `json:"ips"`
	Hostname      string     `json:"hostname"`
	OSVersion     string     `json:"osVersion"`
	UptimeSeconds int64      `json:"uptimeSeconds"`
	ProcessCount  int        `json:"processCount"`
	GoVersion     string     `json:"goVersion"`
	NetDownSpeedBps uint64   `json:"netDownSpeedBps"`
	NetUpSpeedBps   uint64   `json:"netUpSpeedBps"`
	NetInterface    string   `json:"netInterface"`
}

// DiskInfo 单块磁盘使用情况
type DiskInfo struct {
	Name    string  `json:"name"`
	UsedGB  float64 `json:"usedGB"`
	TotalGB float64 `json:"totalGB"`
	Percent float64 `json:"percent"`
}

// GetSystemStatus 采集系统资源概览
func GetSystemStatus() (*SystemStatus, error) {
	st := &SystemStatus{}
	st.CPUPercent = cpuPercent()
	used, total := memoryInfo()
	st.MemUsedGB = used
	st.MemTotalGB = total
	if total > 0 {
		st.MemPercent = used / total * 100
	}
	st.Disks = diskInfos()
	st.IPs = localIPv4s()
	st.Hostname, _ = os.Hostname()
	st.OSVersion = windowsVersion()
	st.UptimeSeconds = getTickCount64() / 1000
	st.ProcessCount = processCount()
	st.GoVersion = runtime.Version()
	ns := GetNetSpeed()
	st.NetDownSpeedBps = ns.DownBps
	st.NetUpSpeedBps = ns.UpBps
	st.NetInterface = ns.Interface
	return st, nil
}

// ---- CPU ----
func cpuPercent() float64 {
	idle1, k1, u1 := sysTimes()
	time.Sleep(150 * time.Millisecond)
	idle2, k2, u2 := sysTimes()
	total := (k2 + u2) - (k1 + u1)
	idl := idle2 - idle1
	if total == 0 {
		return 0
	}
	return (1 - float64(idl)/float64(total)) * 100
}

func sysTimes() (idle, kernel, user uint64) {
	var i, k, u uint64
	modKernel32.NewProc("GetSystemTimes").
		Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	return i, k, u
}

// ---- 内存 ----
type memoryStatusEx struct {
	cbSize                   uint32
	dwMemoryLoad             uint32
	ullTotalPhys             uint64
	ullAvailPhys             uint64
	ullTotalPageFile         uint64
	ullAvailPageFile         uint64
	ullTotalVirtual          uint64
	ullAvailVirtual          uint64
	ullAvailExtendedVirtual  uint64
}

func memoryInfo() (usedGB, totalGB float64) {
	var m memoryStatusEx
	m.cbSize = uint32(unsafe.Sizeof(m))
	r, _, _ := modKernel32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, 0
	}
	const gb = 1e9
	total := float64(m.ullTotalPhys)
	used := total - float64(m.ullAvailPhys)
	return used / gb, total / gb
}

// ---- 磁盘 ----
func diskInfos() []DiskInfo {
	kernel32 := modKernel32
	buf := make([]uint16, 256)
	kernel32.NewProc("GetLogicalDriveStringsW").Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))

	var drives []string
	for i := 0; i < len(buf); {
		if buf[i] == 0 {
			break
		}
		start := i
		for buf[i] != 0 {
			i++
		}
		drives = append(drives, syscall.UTF16ToString(buf[start:i]))
		i++
	}

	const gb = 1e9
	var out []DiskInfo
	for _, d := range drives {
		var freeAvail, total, freeTotal uint64
		r, _, _ := kernel32.NewProc("GetDiskFreeSpaceExW").Call(
			uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(d))),
			uintptr(unsafe.Pointer(&freeAvail)),
			uintptr(unsafe.Pointer(&total)),
			uintptr(unsafe.Pointer(&freeTotal)),
		)
		if r == 0 || total == 0 {
			continue
		}
		out = append(out, DiskInfo{
			Name:    d,
			UsedGB:  float64(total-freeTotal) / gb,
			TotalGB: float64(total) / gb,
			Percent: float64(total-freeTotal) / float64(total) * 100,
		})
	}
	return out
}

// ---- 网络 ----
func localIPv4s() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				ips = append(ips, v4.String())
			}
		}
	}
	return ips
}

// ---- 主机名 & OS 版本 ----
func windowsVersion() string {
	dll := modNtdll
	proc := dll.NewProc("RtlGetVersion")
	var v struct {
		Major      uint32
		Minor      uint32
		Build      uint32
		_          [4]byte
	}
	v.Major = 10 // dwOSVersionInfoSize 占位
	proc.Call(uintptr(unsafe.Pointer(&v)))
	return fmt.Sprintf("Windows %d.%d (Build %d)", v.Major, v.Minor, v.Build)
}

// ---- 运行时长 ----
func getTickCount64() int64 {
	proc := modKernel32.NewProc("GetTickCount64")
	ret, _, _ := proc.Call()
	return int64(ret)
}

// ---- 进程数 ----
func processCount() int {
	dll := modKernel32
	snap, _, _ := dll.NewProc("CreateToolhelp32Snapshot").Call(2, 0) // TH32CS_SNAPPROCESS = 2
	if snap == uintptr(syscall.InvalidHandle) {
		return 0
	}
	defer dll.NewProc("CloseHandle").Call(snap)

	var pe struct {
		dwSize              uint32
		cntUsage            uint32
		th32ProcessID       uint32
		th32DefaultHeapID   uintptr
		th32ModuleID        uint32
		cntThreads          uint32
		th32ParentProcessID uint32
		pcPriClassBase      int32
		dwFlags             uint32
		szExeFile           [260]uint16
	}
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	count := 0

	proc := dll.NewProc("Process32FirstW")
	ret, _, _ := proc.Call(snap, uintptr(unsafe.Pointer(&pe)))
	if ret == 0 {
		return 0
	}
	count++

	proc = dll.NewProc("Process32NextW")
	for {
		ret, _, _ := proc.Call(snap, uintptr(unsafe.Pointer(&pe)))
		if ret == 0 {
			break
		}
		count++
	}
	return count
}
