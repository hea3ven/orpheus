//go:build linux

package cli

import "testing"

func TestRunningUnderWatchIgnoresLauncherDuringCoverage(t *testing.T) {
	t.Setenv(coverageRunEnvironment, "1")
	original := watchAncestryContains
	t.Cleanup(func() { watchAncestryContains = original })
	called := false
	watchAncestryContains = func(pid int, want string, maxDepth int) bool {
		called = true
		return true
	}

	if runningUnderWatch() {
		t.Fatal("runningUnderWatch reported launcher ancestry during coverage")
	}
	if called {
		t.Fatal("runningUnderWatch inspected launcher ancestry during coverage")
	}
}
