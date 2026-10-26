package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// updState is what the page's update panel shows.
type updState struct {
	Phase   string     `json:"phase"` // idle, checking, checked, running, done
	OK      bool       `json:"ok"`
	Message string     `json:"message"`
	Plan    []planItem `json:"plan"`
	Lines   []string   `json:"lines"`
}

type updater struct {
	mu     sync.Mutex
	st     updState
	busy   bool
	cancel context.CancelFunc
}

func (u *updater) state() updState {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.st
	s.Lines = append([]string(nil), u.st.Lines...)
	if s.Phase == "" {
		s.Phase = "idle"
	}
	return s
}

func (u *updater) message(m string) {
	u.mu.Lock()
	u.st.Message = m
	u.mu.Unlock()
}

// lineWriter collects update output for the page (and a log file).
// Successive progress lines for one download replace each other.
type lineWriter struct {
	u    *updater
	file io.Writer
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w.file != nil {
		_, _ = w.file.Write(p)
	}
	w.u.mu.Lock()
	defer w.u.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line == "" {
			continue
		}
		L := w.u.st.Lines
		if n := len(L); n > 0 && isProgress(line) && isProgress(L[n-1]) && progressKey(line) == progressKey(L[n-1]) {
			L[n-1] = line
		} else {
			L = append(L, line)
		}
		if len(L) > 200 {
			L = L[len(L)-200:]
		}
		w.u.st.Lines = L
	}
	return len(p), nil
}

func isProgress(s string) bool { return strings.Contains(s, "% of ") }

func progressKey(s string) string {
	k, _, _ := strings.Cut(s, ":")
	return k
}

// updateCheck lists what an update would download, without writing anything.
func (a *App) updateCheck() bool {
	u := &a.upd
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return false
	}
	u.busy = true
	u.st = updState{Phase: "checking", Message: "Checking for updates…"}
	u.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		uout = &lineWriter{u: u, file: logFile("update.log")}
		res, err := runUpdate(ctx, a.root(), a.Cfg, updateOpts{DryRun: true})
		uout = os.Stdout
		u.mu.Lock()
		defer u.mu.Unlock()
		u.busy = false
		u.st.Phase = "checked"
		switch {
		case err != nil:
			u.st.OK, u.st.Message = false, err.Error()
		case !res.OK:
			u.st.OK, u.st.Message = false, "Some checks failed - is this computer connected to the internet?"
		default:
			u.st.OK, u.st.Plan, u.st.Message = true, res.Plan, ""
		}
	}()
	return true
}

// updateStart downloads updates while Offline Wiki keeps running. On a Mac
// with the drive read-only it briefly pauses to switch the drive to
// read-write, and again at the end to switch it back.
func (a *App) updateStart() bool {
	u := &a.upd
	u.mu.Lock()
	if u.busy {
		u.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.busy, u.cancel = true, cancel
	u.st = updState{Phase: "running", Message: "Preparing…"}
	u.mu.Unlock()
	go a.runInAppUpdate(ctx)
	return true
}

func (a *App) updateCancel() {
	u := &a.upd
	u.mu.Lock()
	if u.cancel != nil {
		u.cancel()
	}
	u.mu.Unlock()
}

func (a *App) runInAppUpdate(ctx context.Context) {
	u := &a.upd
	w := &lineWriter{u: u, file: logFile("update.log")}
	finish := func(ok bool, msg string) {
		u.mu.Lock()
		u.busy, u.cancel = false, nil
		u.st.Phase, u.st.OK, u.st.Message = "done", ok, msg
		u.mu.Unlock()
	}

	root := a.root()
	wasRO := isReadOnly(root)
	if wasRO {
		if !canRemount(root) {
			finish(false, "The drive is read-only and this computer can't switch it to read-write.")
			return
		}
		u.message("Switching the drive to read-write - Offline Wiki pauses for a moment…")
		a.stopServices()
		nr, err := remount(root, false)
		if nr != "" {
			a.setRoot(nr)
			root = nr
		}
		a.startServices()
		if err != nil {
			finish(false, "Couldn't switch the drive to read-write: "+err.Error())
			return
		}
		a.setDriveNote("an update is writing to the drive")
	}

	u.message("Downloading. Offline Wiki keeps working meanwhile - keep the drive plugged in until this finishes.")
	uout = w
	res, err := runUpdate(ctx, root, a.Cfg, updateOpts{KeepOld: true})
	uout = os.Stdout
	changed := res != nil && res.Changed

	if changed || wasRO {
		u.message("Finishing - restarting Offline Wiki with the new files…")
		a.stopServices()
		if changed {
			removeOldZimsNow(root, a.Cfg, w)
		}
		if wasRO {
			nr, rerr := remount(root, true)
			if nr != "" {
				a.setRoot(nr)
			}
			if rerr != nil {
				fmt.Fprintf(w, "Couldn't switch the drive back to read-only: %v\n", rerr)
				a.setDriveNote("couldn't switch back to read-only - eject the drive before unplugging")
			} else {
				a.setDriveNote("")
			}
		}
		a.startServices()
	}

	switch {
	case ctx.Err() != nil:
		finish(false, "Stopped. Anything partly downloaded carries on from where it left off next time.")
	case err != nil:
		finish(false, "The update failed: "+err.Error())
	case !res.OK:
		finish(false, "Finished with problems - see the details below.")
	case changed:
		finish(true, "Updated. Offline Wiki is using the new files.")
	default:
		finish(true, "Everything was already up to date.")
	}
}
