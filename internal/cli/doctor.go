package cli

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
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
	if _, err := fmt.Fprintln(output, "Agent usage telemetry"); err != nil {
		return err
	}
	if len(result.Rows) == 0 {
		if _, err := fmt.Fprintln(output, "No supported agent usage telemetry issues found."); err != nil {
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
			"CANDIDATE_DETAILS",
			"SESSION",
			"MODEL",
			"TOTAL_TOKENS",
			"ESTIMATED_COST",
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
			formatDoctorAttempt(row.Attempt),
			formatTaskStatsField(row.Step),
			formatTaskStatsField(row.Outcome),
			formatTaskStatsField(row.Reason),
			strconv.Itoa(row.CandidateCount),
			formatDoctorCandidateDetails(row),
			formatTaskStatsField(row.SessionID),
			formatTaskStatsField(row.Model),
			formatDoctorInt(row.TotalTokens),
			formatDoctorCost(row.CostMicroUSD),
			formatTaskStatsField(row.LogPath),
		})
	}
	return rendered
}

func formatDoctorCandidateDetails(row doctor.Row) string {
	if len(row.Candidates) == 0 {
		return "-"
	}
	summaries := make([]string, 0, len(row.Candidates))
	for _, candidate := range row.Candidates {
		parts := make([]string, 0, 7)
		if candidate.SessionID != "" {
			parts = append(parts, "id="+candidate.SessionID)
		}
		if candidate.SessionName != "" {
			parts = append(parts, "name="+candidate.SessionName)
		}
		if !candidate.StartedAt.IsZero() {
			parts = append(parts, "started="+candidate.StartedAt.UTC().Format(time.RFC3339))
		}
		parts = append(parts, "offset="+formatDoctorCandidateOffset(candidate.StartOffsetMillis))
		if candidate.CWD != "" {
			parts = append(parts, "cwd="+candidate.CWD)
		}
		if candidate.Model != "" {
			parts = append(parts, "model="+candidate.Model)
		}
		if candidate.LogPath != "" {
			parts = append(parts, "log="+candidate.LogPath)
		}
		summaries = append(summaries, strings.Join(parts, " "))
	}
	return strings.Join(summaries, "; ")
}

func formatDoctorCandidateOffset(offsetMillis int64) string {
	if offsetMillis < 0 {
		offsetMillis = -offsetMillis
	}
	return (time.Duration(offsetMillis) * time.Millisecond).String()
}

func formatDoctorAttempt(value int) string {
	if value <= 0 {
		return "-"
	}
	return strconv.Itoa(value)
}

func formatDoctorInt(value int) string {
	if value == 0 {
		return "-"
	}
	return strconv.Itoa(value)
}

func formatDoctorCost(value int64) string {
	if value == 0 {
		return "-"
	}
	return agent.FormatUsageCostUSD(value)
}
