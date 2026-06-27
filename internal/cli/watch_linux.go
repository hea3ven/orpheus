//go:build linux

package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func runningUnderWatch() bool {
	return processAncestryContains(os.Getppid(), "watch", 8)
}

func processAncestryContains(pid int, want string, maxDepth int) bool {
	for range maxDepth {
		name, parent, ok := linuxProcessInfo(pid)
		if !ok {
			return false
		}
		if name == want {
			return true
		}
		if parent <= 1 || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

func linuxProcessInfo(pid int) (string, int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "", 0, false
	}

	var name string
	var parent int
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Name":
			name = value
		case "PPid":
			parent, err = strconv.Atoi(value)
			if err != nil {
				return "", 0, false
			}
		}
	}
	return name, parent, name != "" && parent > 0
}
