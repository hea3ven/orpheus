// Package testutil provides shared helpers for repository tests.
package testutil

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/pathutil"
)

// CanonicalTempDir returns a test-owned temporary directory as a clean absolute
// path with all existing symlink components resolved.
func CanonicalTempDir(tb testing.TB) string {
	tb.Helper()

	//nolint:forbidigo // This is the sole boundary for testing.TB.TempDir; it canonicalizes the returned path.
	dir := tb.TempDir()
	canonicalDir, err := pathutil.CanonicalAbs(dir)
	if err != nil {
		tb.Fatalf("canonicalize test temporary directory %q: %v", dir, err)
	}
	return canonicalDir
}
