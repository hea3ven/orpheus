package delivery

import (
	"context"
	"errors"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) nextDirect(current work.State, selected committedPublication) (phaseexec.Selection[Request, Outcome], error) {
	owner, p, w := selected.owner(current), selected.publication, selected.workflow
	prepared := w.PublicPreparation(p)
	if prepared == p {
		selected, err := s.nextPublicPreparation(owner, p)
		if err != nil {
			return phaseexec.Selection[Request, Outcome]{}, err
		}
		if selected != nil {
			return phaseexec.Run[Request, Outcome](*selected), nil
		}
	}
	sync := w.PublicSync(p)
	if sync == nil {
		if s.syncInput != nil {
			return phaseexec.Run[Request, Outcome](s.mergeDirectSync(owner)), nil
		}
		return phaseexec.Run[Request, Outcome](s.beginDirectSync(owner)), nil
	}
	if sync.Result == nil {
		return s.nextResolver(current, selected)
	}
	if sync.Result.Commit != prepared.Operation.Public.Commit && p.Input.Code.CodeIdentity != *sync.Result {
		return phaseexec.Run[Request, Outcome](s.renewSynchronizedInput(owner, *sync.Result, sync.Destination)), nil
	}
	if p.Integration == nil {
		return phaseexec.Run[Request, Outcome](s.prepareIntegration(owner)), nil
	}
	if p.Integration.Result == nil {
		return phaseexec.Selection[Request, Outcome]{}, errors.New("unrecorded integration needs explicit operator restoration or manual repair")
	}
	return phaseexec.Run[Request, Outcome](s.pushIntegration(owner)), nil
}

func (s *Service) describePackageChanges(owner work.AttemptRef) phaseexec.Operation {
	return operation("describe changed public output", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			diff, err := s.DirectGit.DescribePublicChanges(ctx, w.Context().Directory, p.Input.Code.Commit, p.Operation.Public.Commit)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.packageDescribed = true
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.Operation.InputChanges = diff
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) beginDirectSync(owner work.AttemptRef) phaseexec.Operation {
	// Observe the checkout before reserving the destructive merge intent.
	return operation("inspect direct sync input", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			current, err := s.Git.InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.syncInput = &work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}
			return phaseexec.Result{}
		}}, nil
	})
}

func (s *Service) mergeDirectSync(owner work.AttemptRef) phaseexec.Operation {
	return operation("synchronize direct publication", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		input := *s.syncInput
		p.DirectSync = &work.DirectPublicationSync{StartedAt: time.Now().UTC(), Source: input, Destination: p.Destination}
		selected := committedPublication{prepared: w.Context(), destination: &p.Destination}
		opts := directSyncOptions(s.Source.Repository, selected)
		return phaseexec.Prepared{Changed: true, Run: func(ctx context.Context) phaseexec.Result {
			synced, err := s.DirectGit.BeginLocalTaskBranchSync(ctx, opts, p.Destination.Commit)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if synced.Status == gitmeta.TaskBranchSyncConflicted {
				files := append([]string(nil), synced.ConflictFiles...)
				var resolverErr error
				if s.ConflictResolver == nil {
					resolverErr = errors.New("direct sync conflicts require a configured resolver; continue task run for restoration or manual repair")
				} else {
					s.freshConflict = true
				}
				return phaseexec.Result{Err: resolverErr, Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
					p.DirectSync.ConflictFiles = files
					return true, nil
				})}
			}
			current, err := s.Git.InspectPublicationCheckout(ctx, opts.Worktree, opts.Branch)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			observed := work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.DirectSync.Result = &observed
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) renewSynchronizedInput(owner work.AttemptRef, input work.CodeIdentity, destination work.Destination) phaseexec.Operation {
	return operation("select synchronized publication input", owner, func(snapshot *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		selected := committedPublication{workflowIndex: owner.Workflow, phaseIndex: owner.Phase, workflow: w, publication: p, prepared: w.Context()}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if err := s.renewDirectPublication(ctx, selected, input, destination, "Local destination synchronization; result has not been separately task reviewed", true); err != nil {
				return phaseexec.Result{Err: err}
			}
			plan := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			return phaseexec.Result{Update: change(owner, func(_ *work.State, w *work.Workflow, _ *work.PublicationPhase) (bool, error) {
				w.Phases = append(w.Phases, plan)
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) prepareIntegration(owner work.AttemptRef) phaseexec.Operation {
	return operation("prepare direct integration", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		p.Integration = &work.DirectIntegration{StartedAt: time.Now().UTC()}
		req := directIntegrationRequest(s.Source.Repository, w, p)
		return phaseexec.Prepared{Changed: true, Run: func(ctx context.Context) phaseexec.Result {
			result, err := s.DirectGit.PrepareDirectIntegration(ctx, req)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if result.Commit != req.Source.Commit || result.Tree != req.Source.Tree {
				return phaseexec.Result{Err: errors.New("prepared destination differs from synchronized task commit or tree")}
			}
			observed := work.CodeIdentity{Commit: result.Commit, Tree: result.Tree}
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.Integration.Result = &observed
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) pushIntegration(owner work.AttemptRef) phaseexec.Operation {
	return operation("push direct integration", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		req := directIntegrationRequest(s.Source.Repository, w, p)
		result := gitmeta.Revision{Commit: p.Integration.Result.Commit, Tree: p.Integration.Result.Tree}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			delivered, err := s.DirectGit.PushDirectIntegration(ctx, req, result)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if delivered.Commit != result.Commit || delivered.Tree != result.Tree {
				return phaseexec.Result{Err: errors.New("observed destination differs from recorded integration")}
			}
			receipt := &work.DirectDelivery{ObservedAt: time.Now().UTC()}
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.Delivery = receipt
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) nextPublicPreparation(owner work.AttemptRef, p *work.PublicationPhase) (*phaseexec.Operation, error) {
	if p.Operation == nil {
		if s.preflight == nil || !s.preflight.owner.Equal(owner) {
			op := s.publicPreflight(owner, false)
			return &op, nil
		}
		op := s.packagePublic(owner)
		return &op, nil
	}
	if p.Operation.Public == nil {
		return nil, errors.New("publication packaging needs an interactive restore-or-manual-repair decision; continue task run")
	}
	if p.Operation.Public.Tree != p.Input.Code.Tree && p.Operation.InputChanges == "" && !s.packageDescribed {
		op := s.describePackageChanges(owner)
		return &op, nil
	}
	if needsRefRelease(p) {
		op := s.releaseHistory(owner)
		return &op, nil
	}
	if p.Operation.Public.Tree != p.Input.Code.Tree {
		return nil, packageTreeError(p.Input.Code.CodeIdentity, *p.Operation.Public)
	}
	return nil, nil
}
