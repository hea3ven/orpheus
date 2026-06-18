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

type ghStatusByURLCase struct {
	name      string
	output    string
	wantState pullrequest.State
	wantErr   string
}

var ghStatusByURLCases = []ghStatusByURLCase{
	{
		name:      "open",
		output:    `{"url":"https://github.com/org/repo/pull/1","state":"OPEN","mergedAt":null}`,
		wantState: pullrequest.StateOpen,
	},
	{
		name:      "merged timestamp wins",
		output:    `{"url":"https://github.com/org/repo/pull/1","state":"CLOSED","mergedAt":"2026-06-14T10:00:00Z"}`,
		wantState: pullrequest.StateMerged,
	},
	{
		name:      "merged state",
		output:    `{"url":"https://github.com/org/repo/pull/1","state":"MERGED","mergedAt":null}`,
		wantState: pullrequest.StateMerged,
	},
	{
		name:      "closed unmerged",
		output:    `{"url":"https://github.com/org/repo/pull/1","state":"CLOSED","mergedAt":null}`,
		wantState: pullrequest.StateClosed,
	},
	{
		name:    "invalid json",
		output:  `{`,
		wantErr: "provider output was not JSON",
	},
	{
		name:    "invalid url",
		output:  `{"url":"not-a-url","state":"OPEN","mergedAt":null}`,
		wantErr: "valid PR URL",
	},
	{
		name:    "unsupported state",
		output:  `{"url":"https://github.com/org/repo/pull/1","state":"DRAFT","mergedAt":null}`,
		wantErr: "unsupported PR state",
	},
}

func TestGHProviderStatusByURL(t *testing.T) {
	for _, tt := range ghStatusByURLCases {
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

func TestGHProviderStatusByURLRequestsSupportedFields(t *testing.T) {
	logPath := installFakeGH(t, `{"url":"https://github.com/org/repo/pull/1","state":"OPEN","mergedAt":null}`, 0)

	_, err := pullrequest.GHProvider{}.StatusByURL(
		context.Background(),
		pullrequest.StatusByURLRequest{URL: "https://github.com/org/repo/pull/1"},
	)
	if err != nil {
		t.Fatalf("status by URL: %v", err)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gh log: %v", err)
	}
	text := string(logged)
	if !strings.Contains(text, "--json url,state,mergedAt") {
		t.Fatalf("gh args = %q, want mergedAt status fields", text)
	}
	if strings.Contains(text, "--json url,state,merged\n") {
		t.Fatalf("gh args = %q, should not request unsupported merged field", text)
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

func TestGHProviderStatusByURLDoesNotMisclassifyUnknownJSONFieldAsAuth(t *testing.T) {
	installFakeGH(t, "Unknown JSON field: \"merged\"\nAvailable fields:\n  author\n  autoMergeRequest\n", 1)

	_, err := pullrequest.GHProvider{}.StatusByURL(
		context.Background(),
		pullrequest.StatusByURLRequest{URL: "https://github.com/org/repo/pull/1"},
	)
	if err == nil {
		t.Fatal("error = nil, want provider failure")
	}
	if strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %v, should not classify author/autoMergeRequest output as auth failure", err)
	}
	if !strings.Contains(err.Error(), "gh provider command failed") {
		t.Fatalf("error = %v, want generic provider command failure", err)
	}
}

func installFakeGH(t *testing.T, stdout string, exitCode int) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "gh.log")
	stdoutPath := filepath.Join(binDir, "stdout.txt")
	if err := os.WriteFile(stdoutPath, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write fake gh stdout: %v", err)
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"cat " + shellQuote(stdoutPath) + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
