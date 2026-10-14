package testutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestCanonicalTempDirReturnsResolvedAbsolutePath(t *testing.T) {
	physical := testutil.CanonicalTempDir(t)
	alias := filepath.Join(testutil.CanonicalTempDir(t), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatalf("create temporary directory alias: %v", err)
	}
	dir := testutil.CanonicalTempDir(aliasedTempDirectory{TB: t, path: alias})

	if !filepath.IsAbs(dir) {
		t.Fatalf("temporary directory %q is not absolute", dir)
	}
	if filepath.Clean(dir) != dir {
		t.Fatalf("temporary directory %q is not clean", dir)
	}
	if dir != physical {
		t.Fatalf("temporary directory = %q, want physical path %q", dir, physical)
	}
}

type aliasedTempDirectory struct {
	testing.TB
	path string
}

func (d aliasedTempDirectory) TempDir() string { return d.path }
