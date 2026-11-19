//go:build ruleguard

// Loaded by gocritic's Ruleguard checker, not compiled into Orpheus.
package gorules

import (
	"fmt"

	"github.com/quasilyte/go-ruleguard/dsl"
)

// GetType resolves the function in the analyzer's import graph. Compare the
// *types.Signature identity, not signature shape: another package can declare
// a function with the same name and type. Ruleguard's DSL cannot compare Type
// operands with ==, so compare their formatted addresses.
func sameFunction(ctx *dsl.VarFilterContext, name string) bool {
	return fmt.Sprintf("%p", ctx.Type) == fmt.Sprintf("%p", ctx.GetType(name))
}

func isProbePID(ctx *dsl.VarFilterContext) bool {
	return sameFunction(ctx, "github.com/hea3ven/orpheus/internal/agentexec.ProbePID")
}

func isCaptureUsage(ctx *dsl.VarFilterContext) bool {
	return sameFunction(ctx, "github.com/hea3ven/orpheus/internal/agent.CaptureUsage")
}

func isRunPipeline(ctx *dsl.VarFilterContext) bool {
	return sameFunction(ctx, "github.com/hea3ven/orpheus/internal/workflow/review.RunPipeline")
}

func isShortStatus(ctx *dsl.VarFilterContext) bool {
	return sameFunction(ctx, "github.com/hea3ven/orpheus/internal/git.ShortStatus")
}

func wiredCollaborator(m dsl.Matcher) {
	m.Import("github.com/hea3ven/orpheus/internal/agent")
	m.Import("github.com/hea3ven/orpheus/internal/agentexec")
	m.Import("github.com/hea3ven/orpheus/internal/git")
	m.Import("github.com/hea3ven/orpheus/internal/workflow/delivery")
	m.Import("github.com/hea3ven/orpheus/internal/workflow/externalreview")
	m.Import("github.com/hea3ven/orpheus/internal/workflow/implementation")
	m.Import("github.com/hea3ven/orpheus/internal/workflow/preparation")
	m.Import("github.com/hea3ven/orpheus/internal/workflow/review")

	// Invocation setup and options normalization own production defaults.
	execution := func() bool {
		return m.File().PkgPath.Matches(`^github\.com/hea3ven/orpheus/internal/(agent|cli|doctor|workflow(/[^ ]+)?)$`) &&
			!m.File().Name.Matches(`_test\.go$`) &&
			!(m.File().PkgPath.Matches(`/internal/cli$`) && m.File().Name.Matches(`^(invocation|command_options)\.go$`)) &&
			!(m.File().PkgPath.Matches(`/(internal/doctor|workflow/review)$`) && m.File().Name.Matches(`^effects\.go$`))
	}

	m.Match(`$adapter{$*fields}`, `new($adapter)`).
		Where(execution() && (m["adapter"].Type.Is("preparation.LocalGit") ||
			m["adapter"].Type.Is("implementation.LocalOutputGit") ||
			m["adapter"].Type.Is("agent.LocalGitState") ||
			m["adapter"].Type.Is("review.LocalReviewCandidateGit") ||
			m["adapter"].Type.Is("delivery.LocalGit") ||
			m["adapter"].Type.Is("externalreview.LocalGit") ||
			m["adapter"].Type.Is("git.LocalClosedTaskWorktreeGit") ||
			m["adapter"].Type.Is("agentexec.AttachedLauncher"))).
		Report("use the wired collaborator instead of constructing $adapter in execution code")

	qualifiedFunction := func() bool {
		return execution() && m["pkg"].Object.Is("PkgName") && m["$$"].Object.Is("Func")
	}
	m.Match(`$pkg.ProbePID`).
		Where(qualifiedFunction() && m.File().Imports("github.com/hea3ven/orpheus/internal/agentexec") && m["$$"].Filter(isProbePID)).
		Report("use the wired collaborator instead of selecting $$ in execution code")
	m.Match(`$pkg.CaptureUsage`).
		Where(qualifiedFunction() && m.File().Imports("github.com/hea3ven/orpheus/internal/agent") && m["$$"].Filter(isCaptureUsage)).
		Report("use the wired collaborator instead of selecting $$ in execution code")
	m.Match(`$pkg.RunPipeline`).
		Where(qualifiedFunction() && m.File().Imports("github.com/hea3ven/orpheus/internal/workflow/review") && m["$$"].Filter(isRunPipeline)).
		Report("use the wired collaborator instead of selecting $$ in execution code")
	m.Match(`$pkg.ShortStatus`).
		Where(qualifiedFunction() && m.File().Imports("github.com/hea3ven/orpheus/internal/git") && m["$$"].Filter(isShortStatus)).
		Report("use the wired collaborator instead of selecting $$ in execution code")
}
