package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// uout receives update progress: the terminal, or the page's update panel.
var uout io.Writer = os.Stdout

func uprintf(format string, a ...any) { fmt.Fprintf(uout, format, a...) }

type planItem struct {
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
}

type updateOpts struct {
	DryRun  bool
	IfDue   bool
	KeepOld bool // Offline Wiki has the library open: keep superseded files for now
	Only    map[string]bool
}

type updateResult struct {
	Time    time.Time  `json:"time"`
	OK      bool       `json:"ok"`
	Changed bool       `json:"changed"`
	Summary []string   `json:"summary"`
	Plan    []planItem `json:"plan,omitempty"`
}

func lastUpdatePath(root string) string { return filepath.Join(root, "logs", "last-update.json") }

func readLastUpdate(root string) (updateResult, bool) {
	var r updateResult
	b, err := os.ReadFile(lastUpdatePath(root))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}

func lastUpdateSummary(root string) string {
	r, ok := readLastUpdate(root)
	if !ok {
		return "never"
	}
	s := r.Time.Local().Format("2 Jan 2006 15:04")
	if !r.OK {
		s += " (with errors)"
	}
	return s
}

func cmdUpdate(root string, args []string) error {
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	o := updateOpts{Only: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--if-due":
			o.IfDue = true
		case "--dry-run":
			o.DryRun = true
		case "--only":
			if i+1 < len(args) {
				for _, p := range strings.Split(args[i+1], ",") {
					o.Only[strings.TrimSpace(p)] = true
				}
				i++
			}
		default:
			return fmt.Errorf("unknown option %s", args[i])
		}
	}

	if o.IfDue {
		interval := time.Duration(cfg.UpdateIntervalDays) * 24 * time.Hour
		if r, ok := readLastUpdate(root); ok && r.OK && time.Since(r.Time) < interval {
			fmt.Printf("Last update %s; next due in %s. Nothing to do.\n", r.Time.Local().Format("2 Jan 15:04"), (interval - time.Since(r.Time)).Round(time.Hour))
			return nil
		}
		if runningPid() > 0 {
			fmt.Println("Offline Wiki is in use; skipping the automatic update.")
			return nil
		}
		if isReadOnly(root) {
			fmt.Println("The drive is read-only; skipping the automatic update.")
			return nil
		}
	}

	wasRO := false
	if !o.DryRun && isReadOnly(root) {
		if runningPid() > 0 {
			return fmt.Errorf("Offline Wiki is running from this drive - use the Update button in its page, or press Stop there and run this again")
		}
		if !canRemount(root) {
			return fmt.Errorf("the drive is mounted read-only")
		}
		fmt.Println("Switching the drive to read-write for the update…")
		nr, err := remount(root, false)
		if err != nil {
			if nr == "" {
				return fmt.Errorf("%v - unplug the drive and plug it back in", err)
			}
			return err
		}
		root, wasRO = nr, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nStopping - downloads resume next time.")
		cancel()
	}()

	o.KeepOld = runningPid() > 0
	res, err := runUpdate(ctx, root, cfg, o)
	if wasRO {
		fmt.Println("Switching the drive back to read-only…")
		if _, rerr := remount(root, true); rerr != nil {
			fmt.Println("Couldn't switch back to read-only:", rerr, "- eject the drive before unplugging.")
		} else {
			fmt.Println("The drive is read-only again: safe to unplug.")
		}
	}
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("update finished with errors")
	}
	fmt.Println("Update finished.")
	return nil
}

func lockPath() string { return filepath.Join(runtimeDir(), "update.lock") }

// lockHolder returns the process id of another running update, or 0.
func lockHolder() int {
	b, err := os.ReadFile(lockPath())
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	fi, _ := os.Stat(lockPath())
	if pid > 0 && pid != os.Getpid() && processAlive(pid) && fi != nil && time.Since(fi.ModTime()) < 48*time.Hour {
		return pid
	}
	return 0
}

// lock stops two updates running at once on this computer.
func lock() (func(), error) {
	if pid := lockHolder(); pid > 0 {
		return nil, fmt.Errorf("another update is already running (process %d)", pid)
	}
	if err := os.WriteFile(lockPath(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(lockPath()) }, nil
}

type upRun struct {
	ctx  context.Context
	root string
	cfg  Config
	o    updateOpts
	res  *updateResult
}

func (r *upRun) note(ok bool, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(uout, msg)
	r.res.Summary = append(r.res.Summary, msg)
	if !ok {
		r.res.OK = false
	}
}

func (r *upRun) plan(label string, n int64) {
	r.res.Plan = append(r.res.Plan, planItem{Label: label, Bytes: n})
	if n > 0 {
		r.note(true, "%s: would download (%s)", label, human(n))
	} else {
		r.note(true, "%s: would download", label)
	}
}

// runUpdate downloads (or, with DryRun, lists) everything that is missing or
// out of date. Output goes to uout.
func runUpdate(ctx context.Context, root string, cfg Config, o updateOpts) (*updateResult, error) {
	r := &upRun{ctx: ctx, root: root, cfg: cfg, o: o, res: &updateResult{Time: time.Now(), OK: true}}
	want := func(part string) bool { return len(o.Only) == 0 || o.Only[part] }
	if !o.DryRun {
		unlock, err := lock()
		if err != nil {
			return nil, err
		}
		defer unlock()
		ensureMarkers(root)
	}
	uprintf("Offline Wiki update - %s\nDrive: %s\n", time.Now().Format("2 Jan 2006 15:04"), root)

	if want("tools") {
		if err := r.kiwixTools(); err != nil {
			r.note(false, "kiwix-serve: %v", err)
		}
		if err := r.llamafile(); err != nil {
			r.note(false, "llamafile: %v", err)
		}
	}
	if want("models") {
		models := append([]Model(nil), cfg.Models...)
		if cfg.Reranker.File != "" {
			models = append(models, Model{ID: "reranker", File: cfg.Reranker.File, URL: cfg.Reranker.URL})
		}
		for _, m := range models {
			if err := r.model(m); err != nil {
				r.note(false, "model %s: %v", m.ID, err)
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if want("books") {
		for _, b := range cfg.Books {
			if !b.Enabled || ctx.Err() != nil {
				continue
			}
			if b.Local {
				r.note(true, "%s: made on this computer, not downloaded - nothing to update", b.Label)
				continue
			}
			if err := r.book(b); err != nil {
				r.note(false, "%s: %v", b.Label, err)
			}
		}
	}
	if ctx.Err() != nil {
		r.res.OK = false
	}
	if !o.DryRun && len(o.Only) == 0 {
		b, _ := json.MarshalIndent(r.res, "", "  ")
		_ = os.MkdirAll(filepath.Join(root, "logs"), 0o755)
		_ = os.WriteFile(lastUpdatePath(root), b, 0o644)
	}
	return r.res, nil
}

// --- Books -------------------------------------------------------------

var zimDateRe = regexp.MustCompile(`_(\d{4}-\d{2}[a-z]?)\.zim$`)

func latestRemoteZim(ctx context.Context, cfg Config, b Book) (string, error) {
	listing, err := getText(ctx, strings.TrimRight(cfg.ZimMirror, "/")+"/"+b.Dir+"/")
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`href="(` + regexp.QuoteMeta(b.Prefix) + `_(\d{4}-\d{2}[a-z]?)\.zim)"`)
	best, bestDate := "", ""
	for _, m := range re.FindAllStringSubmatch(listing, -1) {
		if m[2] > bestDate {
			best, bestDate = m[1], m[2]
		}
	}
	if best == "" {
		return "", fmt.Errorf("no %s files listed on the server", b.Prefix)
	}
	return best, nil
}

func (r *upRun) book(b Book) error {
	name, err := latestRemoteZim(r.ctx, r.cfg, b)
	if err != nil {
		return err
	}
	lib := filepath.Join(r.root, "library")
	dst := filepath.Join(lib, name)
	if _, err := os.Stat(dst); err == nil {
		r.note(true, "%s: up to date (%s)", b.Label, name)
		r.removeOld(b, name)
		return nil
	}
	url := strings.TrimRight(r.cfg.ZimMirror, "/") + "/" + b.Dir + "/" + name
	size, err := remoteSize(r.ctx, url)
	if err != nil {
		return err
	}
	var have int64
	if fi, err := os.Stat(dst + ".dl.part"); err == nil {
		have = fi.Size()
	}
	free, ferr := diskFree(r.root)
	if ferr == nil && free < uint64(size-have)+1<<30 {
		return fmt.Errorf("not enough space for %s (%s needed, %s free)", name, human(size-have), human(int64(free)))
	}
	date := ""
	if m := zimDateRe.FindStringSubmatch(name); m != nil {
		date = m[1]
	}
	if r.o.DryRun {
		r.plan(fmt.Sprintf("%s %s", b.Label, date), size-have)
		return nil
	}
	_ = os.MkdirAll(lib, 0o755)
	uprintf("%s: downloading %s (%s)\n", b.Label, name, human(size))
	digest, derr := fetchDigest(r.ctx, url+".sha256")
	if err := download(r.ctx, url, dst+".dl", b.Label); err != nil {
		return err
	}
	// download() renames .dl.part to .dl; verify before it goes live.
	if derr == nil {
		uprintf("%s: verifying…\n", b.Label)
		if err := checksum(dst+".dl", digest); err != nil {
			_ = os.Remove(dst + ".dl")
			return err
		}
	} else {
		uprintf("%s: no checksum published (%v); skipping verification\n", b.Label, derr)
	}
	if err := os.Rename(dst+".dl", dst); err != nil {
		return err
	}
	r.res.Changed = true
	r.note(true, "%s: updated to %s", b.Label, name)
	r.removeOld(b, name)
	return nil
}

func oldZims(root string, b Book, keep string) []string {
	entries, _ := os.ReadDir(filepath.Join(root, "library"))
	re := regexp.MustCompile("^" + regexp.QuoteMeta(b.Prefix) + `_\d{4}-\d{2}[a-z]?\.zim$`)
	var out []string
	for _, e := range entries {
		if e.Name() != keep && re.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func (r *upRun) removeOld(b Book, keep string) {
	if r.o.DryRun {
		return
	}
	for _, f := range oldZims(r.root, b, keep) {
		if r.o.KeepOld {
			r.note(true, "%s: old file %s will be removed when Offline Wiki restarts", b.Label, f)
			continue
		}
		if os.Remove(filepath.Join(r.root, "library", f)) == nil {
			r.note(true, "%s: removed old file %s", b.Label, f)
		}
	}
}

// removeOldZimsNow deletes superseded library files. Offline Wiki must have
// closed them first.
func removeOldZimsNow(root string, cfg Config, w io.Writer) {
	for _, z := range localZims(root, cfg) {
		for _, f := range oldZims(root, z.Book, z.File) {
			if os.Remove(filepath.Join(root, "library", f)) == nil {
				fmt.Fprintf(w, "%s: removed old file %s\n", z.Book.Label, f)
			}
		}
	}
}

// --- Models ------------------------------------------------------------

func (r *upRun) model(m Model) error {
	dst := filepath.Join(r.root, "models", m.File)
	if _, err := os.Stat(dst); err == nil {
		r.note(true, "model %s: present", m.ID)
		return nil
	}
	size, err := remoteSize(r.ctx, m.URL)
	if err != nil {
		return err
	}
	if free, err := diskFree(r.root); err == nil && free < uint64(size)+1<<30 {
		return fmt.Errorf("not enough space (%s needed, %s free)", human(size), human(int64(free)))
	}
	if r.o.DryRun {
		label := "Language model (" + m.ID + ")"
		if m.ID == "reranker" {
			label = "Search reranker model"
		}
		r.plan(label, size)
		return nil
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	uprintf("model %s: downloading %s (%s)\n", m.ID, m.File, human(size))
	if err := download(r.ctx, m.URL, dst, "model "+m.ID); err != nil {
		return err
	}
	r.res.Changed = true
	r.note(true, "model %s: downloaded", m.ID)
	return nil
}

// --- llamafile ---------------------------------------------------------

func (r *upRun) llamafile() error {
	body, err := getText(r.ctx, "https://api.github.com/repos/"+r.cfg.LlamafileRepo+"/releases/latest")
	if err != nil {
		return err
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(body), &rel); err != nil {
		return err
	}
	re := regexp.MustCompile(`^llamafile-v?\d+\.\d+\.\d+$`)
	var url, name string
	var size int64
	for _, a := range rel.Assets {
		if re.MatchString(a.Name) {
			url, name, size = a.URL, a.Name, a.Size
		}
	}
	if url == "" {
		return fmt.Errorf("release %s has no plain llamafile binary", rel.TagName)
	}
	bin := filepath.Join(r.root, "bin", "llamafile")
	verFile := bin + ".version"
	if cur, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(cur)) == name {
		if _, err := os.Stat(bin); err == nil {
			r.note(true, "llamafile: up to date (%s)", name)
			return nil
		}
	}
	if r.o.DryRun {
		r.plan("Model runner ("+name+")", size)
		return nil
	}
	if err := download(r.ctx, url, bin+".new", "llamafile"); err != nil {
		return err
	}
	_ = os.Chmod(bin+".new", 0o755)
	_ = os.Remove(bin)
	if err := os.Rename(bin+".new", bin); err != nil {
		return err
	}
	_ = os.Remove(bin + ".exe") // left by older versions
	_ = os.WriteFile(verFile, []byte(name+"\n"), 0o644)
	r.res.Changed = true
	r.note(true, "llamafile: installed %s", name)
	return nil
}

// --- kiwix-serve -------------------------------------------------------

type toolRelease struct {
	file string
	ver  [4]int
}

func (r *upRun) kiwixTools() error {
	listing, err := getText(r.ctx, r.cfg.ToolsMirror)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`href="(kiwix-tools_([a-z0-9_]+-[a-z0-9_]+)-(\d+)\.(\d+)\.(\d+)(?:-(\d+))?\.(?:tar\.gz|zip))"`)
	best := map[string]toolRelease{}
	for _, m := range re.FindAllStringSubmatch(listing, -1) {
		var v [4]int
		for i := 0; i < 4; i++ {
			v[i], _ = strconv.Atoi(m[3+i])
		}
		cur, ok := best[m[2]]
		if !ok || lessVer(cur.ver, v) {
			best[m[2]] = toolRelease{file: m[1], ver: v}
		}
	}
	keys := make([]string, 0, len(kiwixPlatforms))
	for k := range kiwixPlatforms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rel, ok := best[kiwixPlatforms[key]]
		if !ok {
			r.note(false, "kiwix-serve %s: no release found", key)
			continue
		}
		dir := filepath.Join(r.root, "bin", "kiwix", key)
		verFile := filepath.Join(dir, "VERSION")
		if cur, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(cur)) == rel.file {
			r.note(true, "kiwix-serve %s: up to date", key)
			continue
		}
		if r.o.DryRun {
			size, _ := remoteSize(r.ctx, r.cfg.ToolsMirror+rel.file)
			r.plan(fmt.Sprintf("Library reader for %s (%d.%d.%d)", key, rel.ver[0], rel.ver[1], rel.ver[2]), size)
			continue
		}
		if err := installKiwixArchive(r.ctx, r.cfg.ToolsMirror+rel.file, dir, key); err != nil {
			r.note(false, "kiwix-serve %s: %v", key, err)
			continue
		}
		_ = os.WriteFile(verFile, []byte(rel.file+"\n"), 0o644)
		r.res.Changed = true
		r.note(true, "kiwix-serve %s: installed %s", key, rel.file)
	}
	return nil
}

func lessVer(a, b [4]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func installKiwixArchive(ctx context.Context, url, dir, label string) error {
	tmp := dir + ".download"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, filepath.Base(url))
	if err := download(ctx, url, archive, "kiwix-serve "+label); err != nil {
		return err
	}
	if d, err := fetchDigest(ctx, url+".md5"); err == nil {
		if err := checksum(archive, d); err != nil {
			return err
		}
	}
	out := filepath.Join(tmp, "x")
	var err error
	if strings.HasSuffix(url, ".zip") {
		err = unzip(archive, out)
	} else {
		err = untargz(archive, out)
	}
	if err != nil {
		return err
	}
	// Keep everything beside kiwix-serve (Windows builds ship DLLs with it).
	var serveDir string
	filepath.Walk(out, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && (fi.Name() == "kiwix-serve" || fi.Name() == "kiwix-serve.exe") {
			serveDir = filepath.Dir(p)
		}
		return nil
	})
	if serveDir == "" {
		return fmt.Errorf("kiwix-serve not found in %s", filepath.Base(url))
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, _ := os.ReadDir(serveDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(serveDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func safeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %s", name)
	}
	return p, nil
}

func untargz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p, err := safeJoin(dst, h.Name)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
}

func unzip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		p, err := safeJoin(dst, zf.Name)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
