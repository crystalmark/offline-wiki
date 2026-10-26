//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGlobalMemStatus  = kernel32.NewProc("GlobalMemoryStatusEx")
	procOpenProcess      = kernel32.NewProc("OpenProcess")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
	procGetVolumeInfo    = kernel32.NewProc("GetVolumeInformationW")
)

func diskFree(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	r, _, e := procGetDiskFreeSpace.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0, e
	}
	return free, nil
}

type memStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func totalRAMGB() int {
	var m memStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemStatus.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return int(m.TotalPhys / (1 << 30))
}

func openBrowser(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func processAlive(pid int) bool {
	const queryLimited = 0x1000
	h, _, _ := procOpenProcess.Call(queryLimited, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	procCloseHandle.Call(h)
	return true
}

// isReadOnly reports whether the volume holding path is read-only.
func isReadOnly(path string) bool {
	p, err := syscall.UTF16PtrFromString(filepath.VolumeName(path) + `\`)
	if err != nil {
		return false
	}
	var flags uint32
	r, _, _ := procGetVolumeInfo.Call(uintptr(unsafe.Pointer(p)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&flags)), 0, 0)
	return r != 0 && flags&0x00080000 != 0 // FILE_READ_ONLY_VOLUME
}
