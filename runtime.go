package main

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// runtimeDir is a folder on the computer (not the drive) for logs, the
// running-instance marker and the update lock, so normal use never writes
// to the drive.
func runtimeDir() string {
	d := os.Getenv("OFFLINE_WIKI_RUNTIME")
	if d == "" {
		if c, err := os.UserCacheDir(); err == nil {
			d = filepath.Join(c, "OfflineWiki")
		} else {
			d = filepath.Join(os.TempDir(), "OfflineWiki")
		}
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

func logFile(name string) io.Writer {
	f, err := os.OpenFile(filepath.Join(runtimeDir(), name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return io.Discard
	}
	return f
}

func pidPath() string { return filepath.Join(runtimeDir(), "run.pid") }

// runningPid returns the process id of another running Offline Wiki, or 0.
func runningPid() int {
	b, err := os.ReadFile(pidPath())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 && pid != os.Getpid() && processAlive(pid) {
		return pid
	}
	return 0
}

func writePid() { _ = os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o644) }

func removePid() {
	if b, err := os.ReadFile(pidPath()); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(pidPath())
	}
}

// ensureMarkers asks macOS not to index or log the drive, and tidies away
// files older versions kept on it. Called only when writing is expected.
func ensureMarkers(root string) {
	p := filepath.Join(root, ".metadata_never_index")
	if _, err := os.Stat(p); err != nil {
		_ = os.WriteFile(p, nil, 0o644)
	}
	d := filepath.Join(root, ".fseventsd")
	_ = os.MkdirAll(d, 0o755)
	if q := filepath.Join(d, "no_log"); fileMissing(q) {
		_ = os.WriteFile(q, nil, 0o644)
	}
	for _, old := range []string{"kiwix.log", "llm.log", "run.pid", "update.lock"} {
		_ = os.Remove(filepath.Join(root, "logs", old))
	}
}

func fileMissing(p string) bool {
	_, err := os.Stat(p)
	return err != nil
}

func modelPrefPath() string { return filepath.Join(runtimeDir(), "model.txt") }

func savedModel() string {
	b, _ := os.ReadFile(modelPrefPath())
	return strings.TrimSpace(string(b))
}

func saveModel(id string) { _ = os.WriteFile(modelPrefPath(), []byte(id+"\n"), 0o644) }
