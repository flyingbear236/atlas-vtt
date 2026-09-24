package main

import (
	"os/exec"
	"syscall"
)

func configureImageProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
