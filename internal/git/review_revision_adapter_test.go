//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractFreshReviewSelectsRetainsAndDescribesBranchTip(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []struct {
		name, relationship string
		change             func(*testing.T, string) []string
	}{
		{name: "advanced", relationship: "additional commits", change: func(t *testing.T, dir string) []string {
			runGit(t, dir, "commit", "--allow-empty", "-m", "First operator change")
			first := strings.TrimSpace(runGit(t, dir, "rev-parse", "--short", "HEAD"))
			runGit(t, dir, "commit", "--allow-empty", "-m", "Second operator change")
			second := strings.TrimSpace(runGit(t, dir, "rev-parse", "--short", "HEAD"))
			return []string{second + " Second operator change", first + " First operator change"}
		}},
		{name: "amended with identical tree", relationship: "amended or rewritten history", change: func(t *testing.T, dir string) []string {
			runGit(t, dir, "commit", "--amend", "-m", "Operator rewrote the handoff")
			short := strings.TrimSpace(runGit(t, dir, "rev-parse", "--short", "HEAD"))
			return []string{short + " Operator rewrote the handoff"}
		}},
		{name: "earlier", relationship: "earlier commit", change: func(t *testing.T, dir string) []string {
			runGit(t, dir, "reset", "--hard", "HEAD^")
			return nil
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := newGitRepo(t)
			runGit(t, dir, "checkout", "-b", "task")
			base := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("implemented\n"), 0o644))
			original, err := git.CaptureRevision(ctx, git.RevisionCapture{
				Directory: dir, Input: git.CheckoutIdentity{Commit: base, Ref: "refs/heads/task"},
				Ref: "refs/orpheus/revisions/task/0/1", Message: "Original implementation",
			})
			require.NoError(t, err)
			wantAdded := scenario.change(t, dir)
			tip := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			tree := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}"))
			require.NotEqual(t, original.Commit, tip)
			request := git.ReviewRevisionRequest{
				Directory: dir, Branch: "task", Ref: "refs/orpheus/revisions/task/0/2", Comparison: original,
			}

			selected, err := git.SelectReviewRevision(ctx, request)

			require.NoError(t, err)
			assert.Equal(t, scenario.relationship, selected.Relationship)
			assert.Equal(t, wantAdded, selected.AddedCommits)
			assert.Equal(t, tip, selected.Revision.Commit)
			assert.Equal(t, tree, selected.Revision.Tree)
			assert.NotEqual(t, original.RetainedRef, selected.Revision.RetainedRef)
			assert.NoError(t, git.VerifyRevision(ctx, dir, git.CheckoutIdentity{Commit: tip, Ref: "refs/heads/task"}, selected.Revision))
			assert.NoError(t, git.VerifyRetainedRevision(ctx, dir, original))
			runGit(t, dir, "reset", "--hard", original.Commit)
			assert.NoError(t, git.VerifyRetainedRevision(ctx, dir, selected.Revision), "selection survives an independent branch move")
		})
	}
}
