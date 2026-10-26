//go:build darwin || linux

package main

import "os/exec"

func setProcAttrs(cmd *exec.Cmd) {}
