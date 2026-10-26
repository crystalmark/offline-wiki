//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func diskInfo(target string) (dev, mnt string, err error) {
	out, err := exec.Command("diskutil", "info", target).Output()
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Device Node":
			dev = strings.TrimSpace(v)
		case "Mount Point":
			mnt = strings.TrimSpace(v)
		}
	}
	if dev == "" {
		return "", "", fmt.Errorf("no disk found for %s", target)
	}
	return dev, mnt, nil
}

// canRemount reports whether root is the top of a separately mounted volume
// that this program can switch between read-only and read-write.
func canRemount(root string) bool {
	_, mnt, err := diskInfo(root)
	return err == nil && mnt != "" && mnt != "/" && filepath.Clean(mnt) == filepath.Clean(root)
}

// remount unmounts the drive and mounts it again read-only or read-write.
// It returns the new mount point, or "" if the drive was left unmounted.
func remount(root string, readOnly bool) (string, error) {
	dev, _, err := diskInfo(root)
	if err != nil {
		return root, err
	}
	var out []byte
	for i := 0; i < 5; i++ {
		if out, err = exec.Command("diskutil", "unmount", dev).CombinedOutput(); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return root, fmt.Errorf("the drive is in use (%s)", strings.TrimSpace(string(out)))
	}
	args := []string{"mount"}
	if readOnly {
		args = append(args, "readOnly")
	}
	args = append(args, dev)
	if out, err := exec.Command("diskutil", args...).CombinedOutput(); err != nil {
		_ = exec.Command("diskutil", "mount", dev).Run() // put it back as it was
		if _, mnt, e := diskInfo(dev); e == nil && mnt != "" {
			return mnt, fmt.Errorf("remount failed (%s)", strings.TrimSpace(string(out)))
		}
		return "", fmt.Errorf("remount failed (%s)", strings.TrimSpace(string(out)))
	}
	_, mnt, err := diskInfo(dev)
	if err != nil || mnt == "" {
		return "", fmt.Errorf("the drive did not come back after remounting")
	}
	return mnt, nil
}
