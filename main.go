// Offline Wiki: ask questions of offline copies of Wikipedia and other
// reference libraries, answered by a local language model.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const usage = `Offline Wiki

Usage:
  offline-wiki [run] [--model small|large] [--lan] [--no-browser] [--writable]
        Start the library and assistant and open it in a browser.
        On a Mac the drive is switched to read-only while in use, so it can
        be unplugged at any time; --writable leaves it as it is.
        --lan lets phones and other computers on the same network use it.
  offline-wiki update [--only tools,models,books] [--if-due] [--dry-run]
        Download or refresh the programs, models and libraries. On a Mac a
        read-only drive is switched to read-write and back again.
  offline-wiki init
        Prepare an empty drive (folders, config.json, marker file).
  offline-wiki status
        Show what is on the drive.
  offline-wiki install-schedule | uninstall-schedule
        (Mac) Update automatically when the drive is plugged in, at most weekly.
  --root DIR   use DIR as the drive root instead of the executable's location.
`

func main() {
	args := os.Args[1:]
	root := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--root" && i+1 < len(args) {
			root = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	r, err := findRoot(root)
	if err != nil {
		fail(err)
	}
	cmd := "run"
	if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
		cmd, rest = rest[0], rest[1:]
	}
	switch cmd {
	case "run":
		err = cmdRun(r, rest)
	case "update":
		err = cmdUpdate(r, rest)
	case "init":
		err = cmdInit(r)
	case "status":
		err = cmdStatus(r)
	case "install-schedule":
		err = cmdInstallSchedule(r)
	case "uninstall-schedule":
		err = cmdUninstallSchedule()
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}

func cmdInit(root string) error {
	for _, d := range []string{"app", "bin", "models", "library", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return err
		}
	}
	marker := filepath.Join(root, "offline-wiki.drive")
	if _, err := os.Stat(marker); err != nil {
		_ = os.WriteFile(marker, []byte("This file marks an Offline Wiki drive for the scheduled updater.\n"), 0o644)
	}
	ensureMarkers(root)
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		if err := writeConfig(root, defaultConfig()); err != nil {
			return err
		}
	}
	fmt.Println("Drive prepared at", root)
	return nil
}

func cmdStatus(root string) error {
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	fmt.Println("Drive:", root)
	if free, err := diskFree(root); err == nil {
		fmt.Println("Free space:", human(int64(free)))
	}
	if isReadOnly(root) {
		fmt.Println("Mounted:    read-only (safe to unplug)")
	} else {
		fmt.Println("Mounted:    read-write (eject before unplugging)")
	}
	fmt.Println("Libraries:")
	zims := localZims(root, cfg)
	have := map[string]bool{}
	for _, z := range zims {
		have[z.Book.Prefix] = true
		fmt.Printf("  %-12s %s  %s\n", z.Book.Label, z.Date, human(z.Size))
	}
	for _, b := range cfg.Books {
		if b.Enabled && !have[b.Prefix] {
			fmt.Printf("  %-12s missing\n", b.Label)
		}
	}
	fmt.Println("Models:")
	for _, m := range cfg.Models {
		st := "missing"
		if fi, err := os.Stat(filepath.Join(root, "models", m.File)); err == nil {
			st = human(fi.Size())
		}
		fmt.Printf("  %-6s %s (%s)\n", m.ID, m.File, st)
	}
	fmt.Println("Programs:")
	for key := range kiwixPlatforms {
		p := filepath.Join(root, "bin", "kiwix", key, "kiwix-serve")
		if key == "windows-amd64" {
			p += ".exe"
		}
		st := "missing"
		if _, err := os.Stat(p); err == nil {
			st = "ok"
		}
		fmt.Printf("  kiwix-serve %-14s %s\n", key, st)
	}
	st := "missing"
	if _, err := os.Stat(filepath.Join(root, "bin", "llamafile")); err == nil {
		st = "ok"
	}
	fmt.Println("  llamafile                  ", st)
	fmt.Println("Last update:", lastUpdateSummary(root))
	return nil
}
