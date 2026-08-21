package testutil_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestCanonicalTempDirReturnsResolvedAbsolutePath(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)

	if !filepath.IsAbs(dir) {
		t.Fatalf("temporary directory %q is not absolute", dir)
	}
	if filepath.Clean(dir) != dir {
		t.Fatalf("temporary directory %q is not clean", dir)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temporary directory %q: %v", dir, err)
	}
	if filepath.Clean(resolved) != dir {
		t.Fatalf("temporary directory = %q, want resolved path %q", dir, filepath.Clean(resolved))
	}
}
