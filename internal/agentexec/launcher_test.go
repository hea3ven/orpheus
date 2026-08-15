//go:build integration

package agentexec_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/testguard"
)

func TestIntegrationAttachedLauncherBlocksSupportedAgentBeforePATHLookup(t *testing.T) {
	assertSupportedAgentBlockedBeforePATHLookup(t)
}

func TestIntegrationCustomNamedTestBinaryRetainsSafetyGate(t *testing.T) {
	if os.Getenv("ORPHEUS_TESTGUARD_CUSTOM_BINARY") == "1" {
		assertSupportedAgentBlockedBeforePATHLookup(t)
		return
	}

	binary := filepath.Join(t.TempDir(), "op-ejt-agentexec-testbin")
	build := exec.Command("go", "test", "-c", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile custom-named test binary: %v\n%s", err, output)
	}

	run := exec.Command(binary, "-test.run", "^TestIntegrationCustomNamedTestBinaryRetainsSafetyGate$")
	run.Env = append(os.Environ(), "ORPHEUS_TESTGUARD_CUSTOM_BINARY=1")
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run custom-named test binary: %v\n%s", err, output)
	}
}

func assertSupportedAgentBlockedBeforePATHLookup(t *testing.T) {
	t.Helper()
	if !testguard.IsTestProcess() {
		t.Fatal("test safety guard is not active in this test binary")
	}

	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "sentinel-ran")
	sentinel := filepath.Join(binDir, "codex")
	if err := os.WriteFile(sentinel, []byte("#!/bin/sh\nprintf invoked > "+marker+"\n"), 0o755); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testguard.FakeAgentEnvKey("codex"), "")

	err := (agentexec.AttachedLauncher{}).Run(context.Background(), agentexec.Command{
		Name:    "codex",
		Harness: "codex",
		Command: "codex",
	}, agentexec.LaunchOptions{Dir: t.TempDir()})

	if err == nil {
		t.Fatal("Run() error = nil, want test safety gate error")
	}
	if !agentexec.IsStartError(err) {
		t.Fatalf("Run() error = %T %v, want StartError", err, err)
	}
	if !strings.Contains(err.Error(), "test safety gate blocked") {
		t.Fatalf("Run() error = %q, want test safety gate detail", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("sentinel was invoked: stat error = %v", statErr)
	}
}

func TestIntegrationAttachedLauncherReportsDirectChildPIDBeforeWait(t *testing.T) {
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 0.01\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	var observed int
	err := (agentexec.AttachedLauncher{}).Run(context.Background(), agentexec.Command{Name: "agent", Command: fake}, agentexec.LaunchOptions{
		Dir: t.TempDir(),
		OnStart: func(pid int) error {
			observed = pid
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if observed <= 0 {
		t.Fatalf("observed PID = %d, want positive direct child PID", observed)
	}
}

func TestIntegrationAttachedLauncherRunsExplicitlyRegisteredFake(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fake-ran")
	fake := filepath.Join(binDir, "pi")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf invoked > "+marker+"\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testguard.FakeAgentEnvKey("pi"), fake)

	err := (agentexec.AttachedLauncher{}).Run(context.Background(), agentexec.Command{
		Name:    "pi",
		Harness: "pi",
		Command: "pi",
	}, agentexec.LaunchOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fake was not invoked: %v", err)
	}
}
