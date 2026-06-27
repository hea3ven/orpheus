//go:build !linux

package cli

func runningUnderWatch() bool {
	return false
}
