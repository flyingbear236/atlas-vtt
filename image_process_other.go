//go:build !windows

package main

import "os/exec"

func configureImageProcess(cmd *exec.Cmd) {}
