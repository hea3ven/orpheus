//go:build !unix

package main

import (
	"os/exec"
	"time"
)

func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
