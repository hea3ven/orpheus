//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package cli

func fileDescriptorIsTerminal(uintptr) bool {
	return false
}
