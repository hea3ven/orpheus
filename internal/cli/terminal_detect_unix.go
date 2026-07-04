//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package cli

import (
	"syscall"
	"unsafe"
)

func fileDescriptorIsTerminal(fd uintptr) bool {
	var size terminalSize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&size)),
	)
	return errno == 0
}
