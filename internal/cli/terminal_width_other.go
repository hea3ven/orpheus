//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package cli

func terminalWidth(fd uintptr) (int, bool) {
	return 0, false
}
