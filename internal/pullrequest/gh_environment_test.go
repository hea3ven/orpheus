//go:build integration

package pullrequest_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationGHProviderUsesScopedBinaryAndEnvironment(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(testutil.CanonicalTempDir(t), "gh")
	if err := testguard.WriteExecutable(binary, []byte("#!/bin/sh\n[ \"$GH_SCOPE\" = isolated ] || exit 17\nprintf '[{\"url\":\"https://github.test/org/repo/pull/1\"}]'\n")); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	provider := pullrequest.GHProvider{Binary: binary, Environment: []string{"GH_SCOPE=isolated"}}
	_, found, err := provider.FindOpenByBranch(context.Background(), pullrequest.FindOpenByBranchRequest{
		RepositoryPath: testutil.CanonicalTempDir(t), HeadBranch: "feature", BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("find pull request: %v", err)
	}
	if !found {
		t.Fatal("pull request was not found")
	}
}
