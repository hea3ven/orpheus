package cli

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hea3ven/orpheus/internal/doctor"
	"github.com/spf13/cobra"
)

func newDoctorCommand(opts *rootOptions) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run local Orpheus diagnostics across registered repositories",
		Long: "Run local Orpheus diagnostics across registered repositories.\n\n" +
			"By default doctor reports diagnostics and proposed repairs without " +
			"mutating local state. Use --fix to persist repairs that Orpheus can " +
			"make safely.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runDoctor(command, opts, fix)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "persist safe high-confidence repairs")
	return cmd
}

func runDoctor(command *cobra.Command, opts *rootOptions, fix bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "doctor"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for doctor")

	registryStore, paths, err := newRegistryStoreWithPathsFromEnvironment()
	if err != nil {
		return err
	}
	reg, err := registryStore.Load()
	if err != nil {
		return err
	}

	result, err := doctor.Run(doctor.Options{
		Paths:    paths,
		Registry: reg,
		Fix:      fix,
	})
	if err != nil {
		return err
	}
	return renderDoctorResult(command.OutOrStdout(), result)
}

func renderDoctorResult(output interface{ Write([]byte) (int, error) }, result doctor.Result) error {
	if _, err := fmt.Fprintln(output, "Codex usage telemetry"); err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		if _, err := fmt.Fprintln(output, "No Codex usage telemetry issues found."); err != nil {
			return err
		}
	} else if err := renderTable(
		output,
		[]string{
			"REPO",
			"TASK",
			"TYPE",
			"ATTEMPT",
			"STEP",
			"OUTCOME",
			"REASON",
			"CANDIDATES",
			"SESSION",
			"MODEL",
			"TOTAL_TOKENS",
			"LOG",
		},
		doctorRows(result.Rows),
	); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(output, "\nSummary"); err != nil {
		return err
	}
	return renderTable(
		output,
		[]string{
			"CHECKED",
			"RECOVERABLE",
			"RECOVERED",
			"UNRESOLVED_UNKNOWNS",
			"AMBIGUOUS",
		},
		[][]string{{
			strconv.Itoa(result.Summary.Checked),
			strconv.Itoa(result.Summary.Recoverable),
			strconv.Itoa(result.Summary.Recovered),
			strconv.Itoa(result.Summary.UnresolvedUnknowns),
			strconv.Itoa(result.Summary.Ambiguous),
		}},
	)
}

func doctorRows(rows []doctor.Row) [][]string {
	rendered := make([][]string, 0, len(rows))
	for _, row := range rows {
		rendered = append(rendered, []string{
			formatTaskStatsField(row.RepoID),
			formatTaskStatsField(row.TaskID),
			formatTaskStatsField(row.Activity),
			strconv.Itoa(row.Attempt),
			formatTaskStatsField(row.Step),
			formatTaskStatsField(row.Outcome),
			formatTaskStatsField(row.Reason),
			strconv.Itoa(row.CandidateCount),
			formatTaskStatsField(row.SessionID),
			formatTaskStatsField(row.Model),
			formatDoctorInt(row.TotalTokens),
			formatTaskStatsField(row.LogPath),
		})
	}
	return rendered
}

func formatDoctorInt(value int) string {
	if value == 0 {
		return "-"
	}
	return strconv.Itoa(value)
}
