package cli

import (
	"github.com/hea3ven/orpheus/internal/revieweval"
	"github.com/spf13/cobra"
)

type reviewContextEvalOptions struct {
	harnesses   []string
	variants    []string
	scenarios   []string
	repetitions int
	complete    bool
	codexModel  string
	piModel     string
	thinking    string
	keepWorkdir bool
}

func newEvalCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run deliberate live evaluations",
		Long: "Run deliberate live evaluations that may launch paid model sessions. " +
			"Evaluation commands are never run by default test or CI targets.",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipInvocationAnnotation: "true"},
		RunE: func(command *cobra.Command, args []string) error {
			return command.Help()
		},
	}
	cmd.AddCommand(newReviewContextEvalCommand(opts))
	return cmd
}

func newReviewContextEvalCommand(_ *rootOptions) *cobra.Command {
	var evalOpts reviewContextEvalOptions
	cmd := &cobra.Command{
		Use:   "review-context",
		Short: "Evaluate legacy and exhaustive review-agent context",
		Long: "Evaluate review-agent context variants against seeded multi-defect scenarios. " +
			"This command launches live Pi or Codex sessions and can incur model costs.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return revieweval.Run(command.Context(), evalOpts.toReviewEvalOptions(), command.OutOrStdout(), command.ErrOrStderr())
		},
	}
	cmd.Flags().StringSliceVar(&evalOpts.harnesses, "harness", nil, "harness selection: pi, codex, all, or comma-separated values")
	cmd.Flags().StringSliceVar(&evalOpts.variants, "variant", nil, "context variant: legacy, exhaustive, all, or comma-separated values")
	cmd.Flags().StringSliceVar(&evalOpts.scenarios, "scenario", nil, "scenario selection: general, architecture, all, or comma-separated values")
	cmd.Flags().IntVar(&evalOpts.repetitions, "repetitions", 1, "number of repetitions for each selected combination")
	cmd.Flags().BoolVar(&evalOpts.complete, "complete", false, "run the full pi/codex x legacy/exhaustive x general/architecture matrix")
	cmd.Flags().StringVar(&evalOpts.codexModel, "codex-model", "", "Codex model for codex harness runs")
	cmd.Flags().StringVar(&evalOpts.piModel, "pi-model", "", "Pi model for pi harness runs")
	cmd.Flags().StringVar(&evalOpts.thinking, "thinking", "", "optional thinking level passed to structured harness profiles")
	cmd.Flags().BoolVar(&evalOpts.keepWorkdir, "keep-workdirs", false, "keep isolated evaluation directories after the report is written")
	return cmd
}

func (o reviewContextEvalOptions) toReviewEvalOptions() revieweval.Options {
	return revieweval.Options{
		Harnesses:    o.harnesses,
		Variants:     o.variants,
		Scenarios:    o.scenarios,
		Repetitions:  o.repetitions,
		Complete:     o.complete,
		CodexModel:   o.codexModel,
		PiModel:      o.piModel,
		Thinking:     o.thinking,
		KeepWorkdirs: o.keepWorkdir,
	}
}
