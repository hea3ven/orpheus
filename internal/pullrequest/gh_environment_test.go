package pullrequest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
)

func TestGHProviderUsesScopedBinaryAndEnvironment(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$GH_SCOPE\" = isolated ] || exit 17\nprintf '[{\"url\":\"https://github.test/org/repo/pull/1\"}]'\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	provider := pullrequest.GHProvider{Binary: binary, Environment: []string{"GH_SCOPE=isolated"}}
	_, found, err := provider.FindOpenByBranch(context.Background(), pullrequest.FindOpenByBranchRequest{
		RepositoryPath: t.TempDir(), HeadBranch: "feature", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("find pull request: %v", err)
	}
	if !found {
		t.Fatal("pull request was not found")
	}
}
