package pullrequest_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
)

func TestGHProviderStatusByURL(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantState pullrequest.State
		wantErr   string
	}{
		{
			name:      "open",
			output:    `{"url":"https://github.com/org/repo/pull/1","state":"OPEN","merged":false}`,
			wantState: pullrequest.StateOpen,
		},
		{
			name:      "merged boolean wins",
			output:    `{"url":"https://github.com/org/repo/pull/1","state":"CLOSED","merged":true}`,
			wantState: pullrequest.StateMerged,
		},
		{
			name:      "merged state",
			output:    `{"url":"https://github.com/org/repo/pull/1","state":"MERGED","merged":false}`,
			wantState: pullrequest.StateMerged,
		},
		{
			name:      "closed unmerged",
			output:    `{"url":"https://github.com/org/repo/pull/1","state":"CLOSED","merged":false}`,
			wantState: pullrequest.StateClosed,
		},
		{
			name:    "invalid json",
			output:  `{`,
			wantErr: "provider output was not JSON",
		},
		{
			name:    "invalid url",
			output:  `{"url":"not-a-url","state":"OPEN","merged":false}`,
			wantErr: "valid PR URL",
		},
		{
			name:    "unsupported state",
			output:  `{"url":"https://github.com/org/repo/pull/1","state":"DRAFT","merged":false}`,
			wantErr: "unsupported PR state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeGH(t, tt.output, 0)
			got, err := pullrequest.GHProvider{}.StatusByURL(
				context.Background(),
				pullrequest.StatusByURLRequest{URL: "https://github.com/org/repo/pull/1"},
			)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("status by URL: %v", err)
			}
			if got.URL != "https://github.com/org/repo/pull/1" || got.State != tt.wantState {
				t.Fatalf("status = %#v, want URL and state %q", got, tt.wantState)
			}
		})
	}
}

func TestGHProviderStatusByURLRejectsMalformedURL(t *testing.T) {
	_, err := pullrequest.GHProvider{}.StatusByURL(
		context.Background(),
		pullrequest.StatusByURLRequest{URL: "not-a-url"},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v, want invalid URL", err)
	}
}

func TestGHProviderStatusByURLWrapsProviderFailure(t *testing.T) {
	installFakeGH(t, "authentication required", 1)
	_, err := pullrequest.GHProvider{}.StatusByURL(
		context.Background(),
		pullrequest.StatusByURLRequest{URL: "https://github.com/org/repo/pull/1"},
	)
	if err == nil || !strings.Contains(err.Error(), "poll GitHub PR https://github.com/org/repo/pull/1") {
		t.Fatalf("error = %v, want polling context", err)
	}
}

func installFakeGH(t *testing.T, stdout string, exitCode int) {
	t.Helper()

	binDir := t.TempDir()
	stdoutPath := filepath.Join(binDir, "stdout.txt")
	if err := os.WriteFile(stdoutPath, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write fake gh stdout: %v", err)
	}
	script := "#!/bin/sh\n" +
		"cat " + shellQuote(stdoutPath) + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
