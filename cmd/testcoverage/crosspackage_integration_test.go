//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture prevents a per-package profile regression: the consumer test must
// credit executable statements in its collaborating package through -coverpkg.
func TestIntegrationCrossPackageCoverageCreditsCollaborator(t *testing.T) {
	fixture := filepath.Join("testdata", "crosspackage")
	profile := filepath.Join(t.TempDir(), "crosspackage.cover")
	command := exec.Command("go", "test", "-count=1", "-tags=integration", "-run", "^TestIntegrationConsumerCreditsCollaborator$", "-covermode=set", "-coverpkg=./...", "-coverprofile="+profile, "./...")
	command.Dir = fixture
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run cross-package fixture: %v\n%s", err, output)
	}
	contents := readProfile(t, profile)
	collaborator := filepath.ToSlash("example.test/crosspackage/collaborator/work.go")
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, collaborator+":") && strings.HasSuffix(line, " 1") {
			return
		}
	}
	t.Fatalf("consumer test did not credit collaborator statements; profile:\n%s", contents)
}

func readProfile(t *testing.T, profile string) string {
	t.Helper()
	contents, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
