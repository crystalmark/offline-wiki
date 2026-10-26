//go:build darwin || linux

package main

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func diskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func totalRAMGB() int {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		return int(n / (1 << 30))
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			return int((kb + (1 << 19)) / (1 << 20))
		}
	}
	return 0
}

func openBrowser(url string) {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("open", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// isReadOnly reports whether the filesystem holding path is mounted read-only.
func isReadOnly(path string) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return false
	}
	return uint64(st.Flags)&1 != 0 // MNT_RDONLY / ST_RDONLY
}
