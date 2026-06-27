package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestStatusRenderOptionsUseWatchWidthWhenStdoutIsNotTerminal(t *testing.T) {
	options := statusRenderOptionsForOutput(
		io.Discard,
		false,
		statusWidthDetector{
			OutputWidth: func(io.Writer) (int, bool) {
				return 0, false
			},
			WatchWidth: func() (int, bool) {
				return 72, true
			},
		},
	)

	if options.NoTruncate {
		t.Fatalf("NoTruncate = true, want false")
	}
	if options.MaxWidth != 72 {
		t.Fatalf("MaxWidth = %d, want 72", options.MaxWidth)
	}
}

func TestStatusRenderOptionsNoTruncateSkipsWidthDetection(t *testing.T) {
	called := false
	options := statusRenderOptionsForOutput(
		io.Discard,
		true,
		statusWidthDetector{
			OutputWidth: func(io.Writer) (int, bool) {
				called = true
				return 80, true
			},
			WatchWidth: func() (int, bool) {
				called = true
				return 72, true
			},
		},
	)

	if !options.NoTruncate {
		t.Fatalf("NoTruncate = false, want true")
	}
	if options.MaxWidth != 0 {
		t.Fatalf("MaxWidth = %d, want 0", options.MaxWidth)
	}
	if called {
		t.Fatalf("width detector was called for no-truncate")
	}
}

func TestRenderStatusResponsiveUsesShortDetailHidesRepoAndTruncatesTitle(t *testing.T) {
	projection := status.Projection{Groups: []status.Group{{
		ID:    status.GroupInReview,
		Title: "Reviewing",
		Entries: []status.Entry{{
			Kind: status.EntryTask,
			Repository: task.Repository{
				ID:           "alpha",
				Name:         "Very Long Repository Name",
				TaskIDPrefix: "op",
			},
			Task: task.Task{
				ID:       "op-123456",
				Priority: 2,
				Title:    "Implement an extremely long operator status title that cannot fit",
			},
			Detail: "https://github.test/org/alpha/pull/123456",
		}},
	}, {
		ID:    status.GroupReadyToRun,
		Title: "Ready to run",
		Entries: []status.Entry{{
			Kind: status.EntryTask,
			Repository: task.Repository{
				ID:           "beta",
				Name:         "Short Repo",
				TaskIDPrefix: "op",
			},
			Task: task.Task{
				ID:       "op-ready",
				Priority: 1,
				Title:    "Ready short",
			},
		}},
	}}}

	var output bytes.Buffer
	err := renderStatus(&output, projection, true, statusRenderOptions{MaxWidth: 48})
	if err != nil {
		t.Fatalf("render status: %v", err)
	}

	got := output.String()
	if strings.Contains(got, "REPO") ||
		strings.Contains(got, "Very Long Repository Name") ||
		strings.Contains(got, "Short Repo") {
		t.Fatalf("responsive output kept repo column:\n%s", got)
	}
	for _, want := range []string{"TASK_ID", "TITLE", "DETAIL", "op-123456", "PR #123456", "..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("responsive output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "https://") {
		t.Fatalf("responsive output kept full PR URL:\n%s", got)
	}
	assertStatusLinesWithinWidth(t, got, 48)
}

func TestRenderStatusNoTruncatePreservesUnboundedOutput(t *testing.T) {
	projection := status.Projection{Groups: []status.Group{{
		ID:    status.GroupInReview,
		Title: "Reviewing",
		Entries: []status.Entry{{
			Kind: status.EntryTask,
			Repository: task.Repository{
				ID:           "alpha",
				Name:         "Very Long Repository Name",
				TaskIDPrefix: "op",
			},
			Task: task.Task{
				ID:       "op-123456",
				Priority: 2,
				Title:    "Implement an extremely long operator status title that cannot fit",
			},
			Detail: "https://github.test/org/alpha/pull/123456",
		}},
	}}}

	var output bytes.Buffer
	err := renderStatus(&output, projection, true, statusRenderOptions{MaxWidth: 48, NoTruncate: true})
	if err != nil {
		t.Fatalf("render status: %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"REPO",
		"Very Long Repository Name",
		"Implement an extremely long operator status title that cannot fit",
		"https://github.test/org/alpha/pull/123456",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unbounded output missing %q:\n%s", want, got)
		}
	}
	if !hasStatusLineWiderThan(got, 48) {
		t.Fatalf("unbounded output unexpectedly fit 48 columns:\n%s", got)
	}
}

func assertStatusLinesWithinWidth(t *testing.T, output string, width int) {
	t.Helper()

	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if displayWidth(line) > width {
			t.Fatalf("line width = %d, want <= %d:\n%s", displayWidth(line), width, output)
		}
	}
}

func hasStatusLineWiderThan(output string, width int) bool {
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if displayWidth(line) > width {
			return true
		}
	}
	return false
}
