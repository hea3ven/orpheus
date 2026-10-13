//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

// This fixture prevents a per-package profile regression: the consumer test must
// credit executable statements in its collaborating package through -coverpkg.
func TestIntegrationBinaryE2ECrossPackageCoverageCreditsCollaborator(t *testing.T) {
	fixture := filepath.Join("testdata", "crosspackage")
	root := testutil.CanonicalTempDir(t)
	profile := filepath.Join(root, "crosspackage.cover")
	binary := filepath.Join(root, "consumer.test")
	if cache := os.Getenv(testBinaryCacheEnvironment); cache != "" {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			t.Fatal(err)
		}
		binary = filepath.Join(cache, "crosspackage-consumer.test")
	}
	// Go revalidates the build inputs on every invocation. A stable output path
	// avoids relinking unchanged inputs after the comparator's excluded warm-up.
	// This contract owns compiler instrumentation and the resulting profile,
	// not Go's test orchestration or a separate no-test collaborator binary.
	build := exec.Command("go", "test", "-c", "-covermode=set", "-coverpkg=./...", "-o", binary, "./consumer")
	build.Dir = fixture
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile cross-package fixture: %v\n%s", err, output)
	}

	command := exec.Command(binary, "-test.run=^TestConsumerCreditsCollaborator$", "-test.coverprofile="+profile)
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
