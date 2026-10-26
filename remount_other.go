//go:build !darwin

package main

import "errors"

func canRemount(root string) bool { return false }

func remount(root string, readOnly bool) (string, error) {
	return root, errors.New("switching the drive between read-only and read-write is only supported on a Mac")
}
