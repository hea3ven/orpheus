package agentexec

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ProcessLiveness is the observable state of a recorded local PID.
type ProcessLiveness string

const (
	ProcessLive    ProcessLiveness = "live"
	ProcessAbsent  ProcessLiveness = "absent"
	ProcessUnknown ProcessLiveness = "unknown"
)

// ProbePID reports whether a local PID still exists. A live PID is deliberately
// treated conservatively because PID reuse cannot be ruled out in this MVP.
func ProbePID(pid int) (ProcessLiveness, error) {
	if pid <= 0 {
		return ProcessUnknown, fmt.Errorf("invalid PID %d", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return ProcessUnknown, err
	}
	if err := process.Signal(syscall.Signal(0)); err == nil || errors.Is(err, syscall.EPERM) {
		return ProcessLive, nil
	} else if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return ProcessAbsent, nil
	} else {
		return ProcessUnknown, err
	}
}
