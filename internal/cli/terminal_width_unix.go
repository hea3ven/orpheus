//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package cli

import (
	"syscall"
	"unsafe"
)

type terminalSize struct {
	Rows    uint16
	Columns uint16
	XPixel  uint16
	YPixel  uint16
}

func terminalWidth(fd uintptr) (int, bool) {
	var size terminalSize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&size)),
	)
	if errno != 0 || size.Columns == 0 {
		return 0, false
	}
	return int(size.Columns), true
}
