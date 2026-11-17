package workflow

import (
	"context"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/work"
)

type candidateReadinessGit struct {
	stat, baseline, comparedRevision string
	err                              error
	dir                              string
	identity                         gitmeta.CheckoutIdentity
	revision                         gitmeta.Revision
}

func (g *candidateReadinessGit) VerifyRevision(_ context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	g.dir, g.identity, g.revision = dir, identity, revision
	return g.err
}
func (g *candidateReadinessGit) DiffStat(_ context.Context, dir, baseline, revision string) (string, error) {
	g.dir, g.baseline, g.comparedRevision = dir, baseline, revision
	return g.stat, g.err
}
func (*candidateReadinessGit) ObserveDestination(_ context.Context, _, branch string) (work.Destination, error) {
	return work.Destination{Remote: "origin", Branch: branch, Commit: "initial-commit"}, nil
}
