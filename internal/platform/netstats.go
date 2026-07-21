package platform

import (
	"net"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Network throughput sampling (bytes/sec), aggregated across non-loopback Up adapters.
// Uses GetIfTable (MIB_IFROW) — uint32 counters. Over a 1s sampling window this does
// not wrap on links ≤ ~34Gbps, which covers all realistic dev/workstation NICs.
// (GetIfTable2's uint64 would avoid wrap entirely but its MIB_IF_ROW2 layout is
// fragile to define in Go; MIB_IFROW offsets are stable and simple.)
var (
	netMu        sync.Mutex
	netLastIn    uint64
	netLastOut   uint64
	netLastTs    time.Time
	netDownBps   uint64
	netUpBps     uint64
	netIfaceName string
)

const (
	ifTypeLoopback = 24
	ifOperStatusUp = 1

	// MIB_IFROW field offsets (4-byte aligned; wszName[256]=512 bytes precedes them)
	offIfIndex    = 512
	offIfType     = 516
	offOperStatus = 544
	offInOctets   = 552
	offOutOctets  = 576
	ifRowSize     = 856 // sizeof(MIB_IFROW)
)

// NetSpeedSample is the latest aggregated network throughput.
type NetSpeedSample struct {
	DownBps   uint64 `json:"downBps"`
	UpBps     uint64 `json:"upBps"`
	Interface string `json:"interface"`
}

// GetNetSpeed returns the current download/upload speed in bytes/sec.
func GetNetSpeed() NetSpeedSample {
	netMu.Lock()
	defer netMu.Unlock()
	return NetSpeedSample{DownBps: netDownBps, UpBps: netUpBps, Interface: netIfaceName}
}

// StartNetStats launches a 1s ticker that samples interface counters and
// computes throughput deltas. Safe to call once at startup.
func StartNetStats() {
	iphlpapi := windows.NewLazySystemDLL("iphlpapi.dll")
	getIfTable := iphlpapi.NewProc("GetIfTable")
	if getIfTable.Find() != nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: [PANIC] netstats: %v\n", r)
			}
		}()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		sampleNet(getIfTable) // prime baseline
		<-ticker.C            // discard first tick
		for range ticker.C {
			sampleNet(getIfTable)
		}
	}()
}

func sampleNet(getIfTable *windows.LazyProc) {
	buf := make([]byte, 16*1024)
	var size uint32 = uint32(len(buf))
	r, _, _ := getIfTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r == 122 { // ERROR_INSUFFICIENT_BUFFER: size now holds required bytes
		buf = make([]byte, size)
		r, _, _ = getIfTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	}
	if r != 0 || len(buf) < 4 {
		return
	}

	numEntries := *(*uint32)(unsafe.Pointer(&buf[0]))
	rows := buf[4:] // table[1] immediately follows dwNumEntries

	// 从 API 返回的 size 反推实际 MIB_IFROW 大小（跨 SDK 版本兼容）
	entrySize := ifRowSize
	if numEntries > 0 && size > 4 {
		if c := int((size - 4) / numEntries); c > 0 && c <= 2048 {
			entrySize = c
		}
	}

	var totalIn, totalOut uint64
	var iface string
	for i := uint32(0); i < numEntries; i++ {
		if int(i)*entrySize >= len(rows) {
			break
		}
		row := rows[int(i)*entrySize:]
		if len(row) < entrySize {
			break
		}
		ifType := *(*uint32)(unsafe.Pointer(&row[offIfType]))
		oper := *(*uint32)(unsafe.Pointer(&row[offOperStatus]))
		if ifType == ifTypeLoopback || oper != ifOperStatusUp {
			continue
		}
		totalIn += uint64(*(*uint32)(unsafe.Pointer(&row[offInOctets])))
		totalOut += uint64(*(*uint32)(unsafe.Pointer(&row[offOutOctets])))
		if iface == "" {
			if idx := int(*(*uint32)(unsafe.Pointer(&row[offIfIndex]))); idx > 0 {
				if ifi, err := net.InterfaceByIndex(idx); err == nil && ifi.Name != "" {
					iface = ifi.Name
				}
			}
		}
	}

	now := time.Now()
	netMu.Lock()
	if !netLastTs.IsZero() && now.After(netLastTs) {
		dt := now.Sub(netLastTs).Seconds()
		if dt > 0 {
			din := int64(totalIn - netLastIn)
			dout := int64(totalOut - netLastOut)
			if din < 0 {
				din = 0 // counter wrapped — ignore this sample
			}
			if dout < 0 {
				dout = 0
			}
			netDownBps = uint64(float64(din) / dt)
			netUpBps = uint64(float64(dout) / dt)
		}
	}
	netLastIn = totalIn
	netLastOut = totalOut
	netLastTs = now
	if iface != "" {
		netIfaceName = iface
	}
	netMu.Unlock()
}
