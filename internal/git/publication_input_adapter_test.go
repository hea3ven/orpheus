//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractPublicationRetainsCleanOperatorHistoryWithoutChangingCheckout(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "repo root", true: "worktree"}[linked], func(t *testing.T) {
			repo := newGitRepo(t)
			dir := repo
			if linked {
				dir = filepath.Join(testutil.CanonicalTempDir(t), "work")
				runGit(t, repo, "worktree", "add", "-b", "task", dir)
			} else {
				runGit(t, dir, "checkout", "-b", "task")
			}
			baseline := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			commitFile(t, dir, "feature.txt", "reviewed\n", "reviewed output")
			reviewed := orpheusgit.Revision{Commit: strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")), Tree: strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}")), RetainedRef: "refs/orpheus/reviewed"}
			runGit(t, dir, "update-ref", reviewed.RetainedRef, reviewed.Commit)
			req := orpheusgit.PublicationInputRequest{Directory: dir, Branch: "task", Destination: "main", Ref: "refs/orpheus/publication", Reviewed: reviewed}
			ordinary, err := orpheusgit.ObservePublicationInput(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, reviewed, ordinary.Revision)
			assert.Equal(t, "reviewed", ordinary.Relationship)
			assert.Empty(t, ordinary.Changes)

			for _, mode := range []string{"added", "amended", "rewritten", "same tree amendment", "unrelated"} {
				t.Run(mode, func(t *testing.T) {
					runGit(t, dir, "reset", "--hard", reviewed.Commit)
					switch mode {
					case "added":
						commitFile(t, dir, "manual.txt", "operator change\n", "operator adds work")
					case "amended":
						require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("operator amendment\n"), 0o644))
						runGit(t, dir, "add", "feature.txt")
						runGit(t, dir, "commit", "--amend", "-m", "operator amends work")
					case "rewritten":
						runGit(t, dir, "reset", "--hard", baseline)
						commitFile(t, dir, "manual.txt", "rewritten operator output\n", "operator rewrites work")
					case "unrelated":
						runGit(t, dir, "checkout", "--orphan", "unrelated-input")
						runGit(t, dir, "rm", "-r", "-f", "--", ".")
						commitFile(t, dir, "manual.txt", "unrelated operator output\n", "operator chooses unrelated history")
						runGit(t, dir, "branch", "-f", "task", "HEAD")
						runGit(t, dir, "checkout", "task")
						runGit(t, dir, "branch", "-D", "unrelated-input")
					case "same tree amendment":
						runGit(t, dir, "commit", "--amend", "-m", "operator changes commit identity")
					}
					head := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
					tree := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}"))
					count := strings.TrimSpace(runGit(t, dir, "rev-list", "--count", "HEAD"))
					actual, err := orpheusgit.ObservePublicationInput(context.Background(), req)
					require.NoError(t, err)
					assert.Equal(t, head, actual.Revision.Commit)
					assert.Equal(t, tree, actual.Revision.Tree)
					assert.Equal(t, req.Ref+"/"+head, actual.Revision.RetainedRef)
					wantRelationship := "amended or rewritten history"
					if mode == "added" {
						wantRelationship = "additional commits after review"
					}
					if mode == "unrelated" {
						wantRelationship = "unrelated history"
					}
					assert.Equal(t, wantRelationship, actual.Relationship)
					if mode == "same tree amendment" {
						assert.Equal(t, reviewed.Tree, actual.Revision.Tree)
						assert.Contains(t, actual.Changes, "No tree differences")
					} else {
						assert.Contains(t, actual.Changes, "operator")
					}
					assert.Equal(t, head, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
					assert.Equal(t, count, strings.TrimSpace(runGit(t, dir, "rev-list", "--count", "HEAD")), "selection must not manufacture a commit")
					assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--short")))
					repeated, err := orpheusgit.ObservePublicationInput(context.Background(), req)
					require.NoError(t, err)
					assert.Equal(t, actual, repeated)
					runGit(t, dir, "reset", "--hard", baseline)
					runGit(t, repo, "reflog", "expire", "--expire=now", "--all")
					runGit(t, repo, "gc", "--prune=now")
					require.NoError(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, actual.Revision), "owned refs retain rewritten operator commits independently of HEAD")
					require.NoError(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, reviewed))
				})
			}
			runGit(t, dir, "reset", "--hard", reviewed.Commit)
			runGit(t, repo, "checkout", "main")
			runGit(t, repo, "merge", "--no-ff", "task", "-m", "retained integration attempt")
			merge := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			req.Accepted, req.AcceptedDestination, req.DeliveryRef = &reviewed, baseline, "refs/orpheus/publication/delivery"
			runGit(t, repo, "update-ref", req.DeliveryRef, merge)
			recovered, err := orpheusgit.ObservePublicationInput(context.Background(), req)
			require.NoError(t, err)
			assert.True(t, recovered.Recovering, "only retained exact merge evidence may preserve the original destination checkpoint")
			assert.Equal(t, reviewed, recovered.Revision)
			runGit(t, repo, "update-ref", req.DeliveryRef, baseline)
			_, err = orpheusgit.ObservePublicationInput(context.Background(), req)
			require.ErrorContains(t, err, "parents differ")
			runGit(t, repo, "update-ref", req.DeliveryRef, merge)
			req.AcceptedDestination = "wrong-parent"
			_, err = orpheusgit.ObservePublicationInput(context.Background(), req)
			require.ErrorContains(t, err, "parents differ")
			req.Accepted, req.DeliveryRef = nil, ""
			if !linked {
				runGit(t, dir, "checkout", "task")
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("do not include\n"), 0o644))
			_, err = orpheusgit.ObservePublicationInput(context.Background(), req)
			require.ErrorContains(t, err, "untracked")
			data, err := os.ReadFile(filepath.Join(dir, "untracked.txt"))
			require.NoError(t, err)
			assert.Equal(t, "do not include\n", string(data))
			require.NoError(t, os.Remove(filepath.Join(dir, "untracked.txt")))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("do not discard\n"), 0o644))
			_, err = orpheusgit.ObservePublicationInput(context.Background(), req)
			require.Error(t, err)
			data, err = os.ReadFile(filepath.Join(dir, "feature.txt"))
			require.NoError(t, err)
			assert.Equal(t, "do not discard\n", string(data))
			runGit(t, dir, "add", "feature.txt")
			_, err = orpheusgit.ObservePublicationInput(context.Background(), req)
			require.ErrorContains(t, err, "index")
			assert.Contains(t, runGit(t, dir, "diff", "--cached"), "do not discard")
		})
	}
}
