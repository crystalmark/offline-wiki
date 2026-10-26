package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

const launchLabel = "uk.co.crystalmark.offline-wiki.update"

// The launchd job runs this script, which looks for any mounted drive
// carrying the offline-wiki marker file and runs its updater if due.
const updateScript = `#!/bin/bash
# Offline Wiki: weekly update of any plugged-in Offline Wiki drive.
# Installed by "offline-wiki install-schedule". Remove with "uninstall-schedule".
LOG="$HOME/Library/Logs/offline-wiki-update.log"
CACHE="$HOME/Library/Caches/OfflineWiki"; mkdir -p "$CACHE"
alive() { [ -f "$1" ] && kill -0 "$(cat "$1" 2>/dev/null)" 2>/dev/null; }
# Leave the drive alone while Offline Wiki is using it or another update runs
# (switching the drive between read-only and read-write also triggers this job).
alive "$CACHE/run.pid" && exit 0
alive "$CACHE/update.lock" && exit 0
case "$(uname -m)" in arm64) PLAT=darwin-arm64 ;; *) PLAT=darwin-amd64 ;; esac
for v in /Volumes/*; do
  [ -f "$v/offline-wiki.drive" ] || continue
  [ -f "$v/app/offline-wiki-$PLAT" ] || continue
  [ -w "$v" ] || continue   # read-only: in use, or left read-only on purpose
  # Don't run on battery power below 30%.
  if pmset -g batt | grep -q "Battery Power"; then
    pct=$(pmset -g batt | grep -oE '[0-9]+%' | head -1 | tr -d %)
    [ -n "$pct" ] && [ "$pct" -lt 30 ] && { echo "$(date): battery low, skipping" >> "$LOG"; exit 0; }
  fi
  # Run a copy from this Mac, so the drive isn't held open by the program.
  B="$CACHE/offline-wiki-scheduled"
  cp "$v/app/offline-wiki-$PLAT" "$B.new" && mv -f "$B.new" "$B" && chmod +x "$B" || continue
  echo "===== $(date) $v =====" >> "$LOG"
  caffeinate -i "$B" --root "$v" update --if-due >> "$LOG" 2>&1
done
`

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>/bin/bash</string><string>%s</string></array>
  <key>StartOnMount</key><true/>
  <key>StartCalendarInterval</key>
  <dict><key>Weekday</key><integer>0</integer><key>Hour</key><integer>3</integer><key>Minute</key><integer>17</integer></dict>
  <key>ProcessType</key><string>Background</string>
  <key>LowPriorityIO</key><true/>
  <key>Nice</key><integer>10</integer>
</dict>
</plist>
`

func scheduleFiles() (script, plist string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	script = filepath.Join(home, "Library", "Application Support", "OfflineWiki", "run-update.sh")
	plist = filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
	return script, plist, nil
}

func cmdInstallSchedule(root string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the scheduled update is set up on the Mac only")
	}
	script, plist, err := scheduleFiles()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(script, []byte(updateScript), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(fmt.Sprintf(plistTemplate, launchLabel, script)), 0o644); err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid, plist).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	fmt.Println("Scheduled update installed.")
	fmt.Println("It runs whenever the drive is plugged in and every Sunday at 03:17,")
	fmt.Println("and only downloads when the last successful update is over a week old.")
	fmt.Println("It skips the drive while Offline Wiki is using it (the drive is then read-only).")
	fmt.Println("Log: ~/Library/Logs/offline-wiki-update.log")
	return nil
}

func cmdUninstallSchedule() error {
	script, plist, err := scheduleFiles()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), plist).Run()
	_ = os.Remove(plist)
	_ = os.Remove(script)
	fmt.Println("Scheduled update removed.")
	return nil
}
