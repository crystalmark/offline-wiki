package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// App holds the running state.
type App struct {
	Root    string
	Cfg     Config
	Zims    []LocalZim
	Model   *Model
	ModelID string // model asked for on the command line, if any

	KiwixPort  int
	LLMPort    int
	UIPort     int
	RerankPort int
	QueryPort  int

	mu        sync.Mutex
	kiwixOK   bool
	llmOK     bool
	rrOK      bool
	qOK       bool // small model for choosing search terms (alongside a thinking model)
	llmErr    string
	kiwixErr  string
	procs     []*proc
	svcCancel context.CancelFunc
	driveNote string // why the drive is writable, when it is

	upd updater
}

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (a *App) root() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Root
}

func (a *App) setRoot(r string) {
	a.mu.Lock()
	a.Root = r
	a.mu.Unlock()
}

func (a *App) setDriveNote(s string) {
	a.mu.Lock()
	a.driveNote = s
	a.mu.Unlock()
}

func (a *App) setKiwix(ok bool, errMsg string) {
	a.mu.Lock()
	a.kiwixOK, a.kiwixErr = ok, errMsg
	a.mu.Unlock()
}

func (a *App) setLLM(ok bool, errMsg string) {
	a.mu.Lock()
	a.llmOK, a.llmErr = ok, errMsg
	a.mu.Unlock()
}

func freePort(host string, want int) int {
	for p := want; p < want+50; p++ {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			l.Close()
			return p
		}
	}
	return want
}

// availableModels lists the models on the drive that fit this computer.
func availableModels(root string, cfg Config) []Model {
	ram := totalRAMGB()
	var out []Model
	for _, m := range cfg.Models {
		if _, err := os.Stat(filepath.Join(root, "models", m.File)); err != nil {
			continue
		}
		if ram > 0 && ram < m.MinRAMGB {
			continue
		}
		out = append(out, m)
	}
	return out
}

// switchModel restarts Offline Wiki's helpers with another model.
func (a *App) switchModel(id string) error {
	ok := false
	for _, m := range availableModels(a.root(), a.Cfg) {
		ok = ok || m.ID == id
	}
	if !ok {
		return fmt.Errorf("model %q isn't available on this computer", id)
	}
	if a.upd.state().Phase == "running" {
		return fmt.Errorf("wait for the update to finish first")
	}
	a.stopServices()
	a.mu.Lock()
	a.ModelID = id
	a.mu.Unlock()
	saveModel(id)
	a.startServices()
	return nil
}

// chooseModel returns the named model if it is on the drive, or else the
// largest non-thinking model that is on the drive and fits this computer.
func chooseModel(root string, cfg Config, want string) *Model {
	ram := totalRAMGB()
	var best *Model
	for i := range cfg.Models {
		m := &cfg.Models[i]
		if _, err := os.Stat(filepath.Join(root, "models", m.File)); err != nil {
			continue
		}
		if want != "" {
			if m.ID == want {
				return m
			}
			continue
		}
		if m.Thinking || (ram > 0 && ram < m.MinRAMGB) {
			continue
		}
		if best == nil || m.MinRAMGB > best.MinRAMGB {
			best = m
		}
	}
	return best
}

// startProc runs a helper program with its log in the runtime folder and its
// working folder off the drive, so the drive can be remounted once it stops.
func (a *App) startProc(ctx context.Context, name, logName string, args ...string) error {
	w := logFile(logName)
	start := func(argv0 string, argv ...string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, argv0, argv...)
		cmd.Stdout, cmd.Stderr = w, w
		cmd.Dir = runtimeDir()
		setProcAttrs(cmd)
		return cmd, cmd.Start()
	}
	cmd, err := start(name, args...)
	if err != nil && errors.Is(err, os.ErrPermission) {
		// The drive may be mounted without execute permission (common for
		// exFAT on Linux): run a copy from this computer instead.
		tmp := filepath.Join(runtimeDir(), "bin", filepath.Base(name))
		_ = os.MkdirAll(filepath.Dir(tmp), 0o755)
		if cerr := copyFile(name, tmp); cerr == nil {
			cmd, err = start(tmp, args...)
		}
	}
	if err != nil && runtime.GOOS != "windows" && isExecFormat(err) {
		// Some Linux setups can't exec APE binaries directly; run via sh.
		cmd, err = start("sh", append([]string{name}, args...)...)
	}
	if err != nil {
		return err
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	a.mu.Lock()
	a.procs = append(a.procs, p)
	a.mu.Unlock()
	return nil
}

func isExecFormat(err error) bool {
	return errors.Is(err, syscall.ENOEXEC) || strings.Contains(err.Error(), "exec format error")
}

func llamafileBinary(root string) (string, error) {
	src := filepath.Join(root, "bin", "llamafile")
	si, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("llamafile is missing from bin/ - run the updater")
	}
	if runtime.GOOS != "windows" {
		return src, nil
	}
	// Windows only runs files ending in .exe: use a copy on this computer.
	dst := filepath.Join(runtimeDir(), "llamafile.exe")
	if di, err := os.Stat(dst); err != nil || di.Size() != si.Size() {
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// startServices starts kiwix-serve and the language model in the background.
func (a *App) startServices() {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.svcCancel = cancel
	a.kiwixOK, a.kiwixErr, a.llmOK, a.llmErr = false, "", false, ""
	a.Zims = localZims(a.Root, a.Cfg)
	a.Model = chooseModel(a.Root, a.Cfg, a.ModelID)
	a.mu.Unlock()
	go a.startKiwix(ctx)
	go a.startLLM(ctx)
	go a.startReranker(ctx)
	go a.startQueryModel(ctx)
}

// stopServices stops both helpers and waits until they have exited, so the
// files they had open on the drive are closed.
func (a *App) stopServices() {
	a.mu.Lock()
	procs := a.procs
	a.procs = nil
	if a.svcCancel != nil {
		a.svcCancel()
	}
	a.kiwixOK, a.llmOK, a.rrOK, a.qOK = false, false, false, false
	a.mu.Unlock()
	for _, p := range procs {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	}
	for _, p := range procs {
		select {
		case <-p.done:
		case <-time.After(15 * time.Second):
		}
	}
}

func (a *App) startKiwix(ctx context.Context) {
	a.mu.Lock()
	root, zims, lan := a.Root, append([]LocalZim(nil), a.Zims...), a.Cfg.LAN
	a.mu.Unlock()
	set := func(ok bool, msg string) {
		if ctx.Err() == nil {
			a.setKiwix(ok, msg)
		}
	}
	bin := filepath.Join(root, "bin", "kiwix", platformKey(), exeName("kiwix-serve"))
	if _, err := os.Stat(bin); err != nil {
		set(false, "kiwix-serve for "+platformKey()+" is missing - run the updater")
		return
	}
	if len(zims) == 0 {
		set(false, "no libraries in library/ - run the updater")
		return
	}
	host := "127.0.0.1"
	if lan {
		host = "all"
	}
	args := []string{"--address=" + host, "--port=" + strconv.Itoa(a.KiwixPort)}
	for _, z := range zims {
		args = append(args, z.Path)
	}
	if err := a.startProc(ctx, bin, "kiwix.log", args...); err != nil {
		set(false, "could not start kiwix-serve: "+err.Error())
		return
	}
	if waitHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/", a.KiwixPort), 60*time.Second) {
		set(true, "")
	} else {
		set(false, "kiwix-serve did not start - see "+filepath.Join(runtimeDir(), "kiwix.log"))
	}
}

func (a *App) startLLM(ctx context.Context) {
	a.mu.Lock()
	root, model := a.Root, a.Model
	a.mu.Unlock()
	set := func(ok bool, msg string) {
		if ctx.Err() == nil {
			a.setLLM(ok, msg)
		}
	}
	if model == nil {
		set(false, "no language model on the drive that fits this computer - answers will be search results only")
		return
	}
	bin, err := llamafileBinary(root)
	if err != nil {
		set(false, err.Error())
		return
	}
	args := []string{
		"--server",
		"-m", filepath.Join(root, "models", model.File),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(a.LLMPort),
		"-ngl", "9999",
		"-c", strconv.Itoa(model.Ctx),
		"--jinja",
	}
	if err := a.startProc(ctx, bin, "llm.log", args...); err != nil {
		set(false, "could not start llamafile: "+err.Error())
		return
	}
	// Loading a large model from a USB drive can take a few minutes.
	if waitHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/health", a.LLMPort), 10*time.Minute) {
		set(true, "")
	} else {
		set(false, "the language model did not start - see "+filepath.Join(runtimeDir(), "llm.log"))
	}
}

// startReranker runs the passage reranker. Without it, answers still work,
// using keyword scores only.
func (a *App) startReranker(ctx context.Context) {
	a.mu.Lock()
	root, file := a.Root, a.Cfg.Reranker.File
	a.mu.Unlock()
	if file == "" {
		return
	}
	path := filepath.Join(root, "models", file)
	if _, err := os.Stat(path); err != nil {
		return
	}
	bin, err := llamafileBinary(root)
	if err != nil {
		return
	}
	args := []string{"--server", "--rerank", "-m", path, "--host", "127.0.0.1", "--port", strconv.Itoa(a.RerankPort),
		"-ngl", "9999", "-c", "8192", "-b", "8192", "-ub", "8192"}
	if err := a.startProc(ctx, bin, "rerank.log", args...); err != nil {
		return
	}
	ok := waitHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/health", a.RerankPort), 3*time.Minute)
	if ctx.Err() == nil {
		a.mu.Lock()
		a.rrOK = ok
		a.mu.Unlock()
	}
}

// startQueryModel runs the small model next to a thinking model, to choose
// search terms quickly: a thinking model deliberates for 15-25 seconds over
// a few keywords, the small model takes about one.
func (a *App) startQueryModel(ctx context.Context) {
	a.mu.Lock()
	root, main, cfg := a.Root, a.Model, a.Cfg
	a.mu.Unlock()
	if main == nil || !main.Thinking {
		return
	}
	var small *Model
	for i := range cfg.Models {
		if cfg.Models[i].ID == "small" {
			small = &cfg.Models[i]
		}
	}
	if small == nil {
		return
	}
	path := filepath.Join(root, "models", small.File)
	if _, err := os.Stat(path); err != nil {
		return
	}
	if ram := totalRAMGB(); ram > 0 && ram < main.MinRAMGB+4 {
		return // not enough memory for both
	}
	bin, err := llamafileBinary(root)
	if err != nil {
		return
	}
	args := []string{"--server", "-m", path, "--host", "127.0.0.1", "--port", strconv.Itoa(a.QueryPort),
		"-ngl", "9999", "-c", "3072", "-np", "1", "--jinja"} // short prompts only: keep its memory small
	if err := a.startProc(ctx, bin, "query.log", args...); err != nil {
		return
	}
	ok := waitHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/health", a.QueryPort), 3*time.Minute)
	if ctx.Err() == nil {
		a.mu.Lock()
		a.qOK = ok
		a.mu.Unlock()
	}
}

func (a *App) Stop() {
	a.updateCancel()
	a.stopServices()
	removePid()
}

// makeReadOnly switches the drive to read-only where possible. It returns a
// note explaining why the drive stays writable (empty if it is read-only)
// and the drive's mount point, which can change when remounted.
func makeReadOnly(root string) (string, string, error) {
	if isReadOnly(root) {
		return "", root, nil
	}
	if !canRemount(root) {
		if runtime.GOOS == "darwin" {
			return "only a drive's top folder can be made read-only", root, nil
		}
		return "this computer can't make the drive read-only - Offline Wiki doesn't write to it, but eject it before unplugging", root, nil
	}
	if pid := lockHolder(); pid > 0 {
		return "an update is running, so the drive stays writable until it finishes - eject it before unplugging", root, nil
	}
	fmt.Println("Switching the drive to read-only…")
	nr, err := remount(root, true)
	if err != nil {
		if nr == "" {
			return "", "", fmt.Errorf("%v - unplug the drive and plug it back in", err)
		}
		return "couldn't make the drive read-only: " + err.Error() + " - eject it before unplugging", nr, nil
	}
	return "", nr, nil
}

func cmdRun(root string, args []string) error {
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	modelID := ""
	noBrowser, writable := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				modelID = args[i+1]
				i++
			}
		case "--lan":
			cfg.LAN = true
		case "--no-browser":
			noBrowser = true
		case "--writable":
			writable = true
		}
	}
	if modelID == "" {
		modelID = savedModel()
		if modelID != "" && chooseModel(root, cfg, modelID) == nil {
			modelID = "" // remembered model not on this drive or too big for this computer
		}
	}
	if pid := runningPid(); pid > 0 {
		return fmt.Errorf("Offline Wiki is already running (process %d) - use its browser tab, or press Stop there first", pid)
	}

	a := &App{Root: root, Cfg: cfg, ModelID: modelID}
	if writable {
		a.driveNote = "started with --writable"
	} else {
		note, nr, err := makeReadOnly(root)
		if err != nil {
			return err
		}
		a.Root, a.driveNote = nr, note
	}

	bindHost := "127.0.0.1"
	if cfg.LAN {
		bindHost = "0.0.0.0"
	}
	a.UIPort = freePort(bindHost, cfg.Port)
	a.KiwixPort = freePort(bindHost, cfg.KiwixPort)
	a.LLMPort = freePort("127.0.0.1", cfg.LLMPort)
	a.RerankPort = freePort("127.0.0.1", cfg.RerankPort)
	a.QueryPort = freePort("127.0.0.1", cfg.QueryPort)
	writePid()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nStopping…")
		a.Stop()
		os.Exit(0)
	}()

	a.startServices()

	url := fmt.Sprintf("http://127.0.0.1:%d/", a.UIPort)
	fmt.Println("Offline Wiki")
	fmt.Println("  Drive:   ", a.Root)
	if isReadOnly(a.Root) {
		fmt.Println("            read-only: safe to unplug at any time")
	} else {
		fmt.Println("            writable:", a.driveNote)
	}
	fmt.Printf("  Library:  %d book(s)\n", len(a.Zims))
	for _, z := range a.Zims {
		fmt.Printf("            %s (%s, %.1f GB)\n", z.Book.Label, z.Date, float64(z.Size)/1e9)
	}
	if a.Model != nil {
		fmt.Printf("  Model:    %s (%s)\n", a.Model.ID, a.Model.File)
	} else {
		fmt.Println("  Model:    none - search-only mode")
	}
	fmt.Println("  Logs:    ", runtimeDir())
	fmt.Println("  Open:    ", url)
	if cfg.LAN {
		fmt.Printf("  Network:  other devices can use http://<this computer's IP>:%d/\n", a.UIPort)
	}
	fmt.Println("  Close this window (or press Stop in the page) to stop.")
	if !noBrowser {
		go func() {
			time.Sleep(700 * time.Millisecond)
			openBrowser(url)
		}()
	}
	err = serveUI(a, net.JoinHostPort(bindHost, strconv.Itoa(a.UIPort)))
	a.Stop()
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o755)
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}
