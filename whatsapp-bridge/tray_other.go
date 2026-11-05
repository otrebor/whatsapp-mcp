//go:build tray && !windows

package main

import "os/exec"

func hideChildConsole(*exec.Cmd) {}
