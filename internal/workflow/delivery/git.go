package delivery

import (
	"context"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/work"
)

// Git consumes committed input. There is deliberately no staging or
// implementation-commit operation at the publication boundary.
type Git interface {
	ObserveDestination(context.Context, string, string) (work.Destination, error)
	ObservePublicationInput(context.Context, gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error)
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	ObservePublicHead(context.Context, string, string) (string, error)
	RetainPublicationInput(context.Context, gitmeta.PublicCommitRequest) error
	InspectPublicationCheckout(context.Context, string, string) (gitmeta.Revision, error)
	InspectPublicationRecovery(context.Context, gitmeta.PublicCommitRequest) (gitmeta.PublicationRecovery, error)
	RestorePublicationInput(context.Context, gitmeta.PublicCommitRequest, gitmeta.PublicationRecovery) error
	ReleasePrivatePublicationRefs(context.Context, string, []string) error
	PackagePublicCommit(context.Context, gitmeta.PublicCommitRequest) (gitmeta.Revision, error)
	PushPublicCommit(context.Context, gitmeta.PublicCommitRequest, gitmeta.Revision) error
	PublishedRevision(context.Context, string, string) (gitmeta.Revision, error)
}

type LocalGit struct{}

func (LocalGit) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	commit, err := gitmeta.ObserveRemoteBranch(ctx, dir, branch)
	if err != nil {
		return work.Destination{}, err
	}
	return work.Destination{Remote: "origin", Branch: branch, Commit: commit}, nil
}
func (LocalGit) ObservePublicationInput(ctx context.Context, req gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error) {
	return gitmeta.ObservePublicationInput(ctx, req)
}
func (LocalGit) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, identity, revision)
}
func (LocalGit) ObservePublicHead(ctx context.Context, dir, branch string) (string, error) {
	return gitmeta.ObservePublicHead(ctx, dir, branch)
}
func (LocalGit) RetainPublicationInput(ctx context.Context, req gitmeta.PublicCommitRequest) error {
	return gitmeta.RetainPublicationInput(ctx, req)
}
func (LocalGit) InspectPublicationCheckout(ctx context.Context, dir, branch string) (gitmeta.Revision, error) {
	return gitmeta.InspectPublicationCheckout(ctx, dir, branch)
}
func (LocalGit) InspectPublicationRecovery(ctx context.Context, req gitmeta.PublicCommitRequest) (gitmeta.PublicationRecovery, error) {
	return gitmeta.InspectPublicationRecovery(ctx, req)
}
func (LocalGit) RestorePublicationInput(ctx context.Context, req gitmeta.PublicCommitRequest, view gitmeta.PublicationRecovery) error {
	return gitmeta.RestorePublicationInput(ctx, req, view)
}
func (LocalGit) ReleasePrivatePublicationRefs(ctx context.Context, dir string, refs []string) error {
	return gitmeta.ReleasePrivatePublicationRefs(ctx, dir, refs)
}
func (LocalGit) PackagePublicCommit(ctx context.Context, req gitmeta.PublicCommitRequest) (gitmeta.Revision, error) {
	return gitmeta.PackagePublicCommit(ctx, req)
}
func (LocalGit) PushPublicCommit(ctx context.Context, req gitmeta.PublicCommitRequest, public gitmeta.Revision) error {
	return gitmeta.PushPublicCommit(ctx, req, public)
}
func (LocalGit) PublishedRevision(ctx context.Context, dir, branch string) (gitmeta.Revision, error) {
	return gitmeta.PublishedRevision(ctx, dir, branch)
}
