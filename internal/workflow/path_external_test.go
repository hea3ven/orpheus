package workflow_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/pathutil"
)

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()

	canonicalPath, err := pathutil.CanonicalAbs(path)
	if err != nil {
		t.Fatalf("canonicalize test path %q: %v", path, err)
	}
	return canonicalPath
}
