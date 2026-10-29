//go:build integration

package agentexec_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractAttachedLauncherBlocksSupportedAgentBeforePATHLookup(t *testing.T) {
	assertSupportedAgentBlockedBeforePATHLookup(t)
}

func assertSupportedAgentBlockedBeforePATHLookup(t *testing.T) {
	t.Helper()
	if !testguard.IsTestProcess() {
		t.Fatal("test safety guard is not active in this test binary")
	}

	binDir := testutil.CanonicalTempDir(t)
	marker := filepath.Join(testutil.CanonicalTempDir(t), "sentinel-ran")
	sentinel := filepath.Join(binDir, "codex")
	if err := testguard.WriteExecutable(sentinel, []byte("#!/bin/sh\nprintf invoked > "+marker+"\n")); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testguard.FakeAgentEnvKey("codex"), "")

	err := (agentexec.AttachedLauncher{}).Run(context.Background(), agentexec.Command{
		Name:    "codex",
		Harness: "codex",
		Command: "codex",
	}, agentexec.LaunchOptions{Dir: testutil.CanonicalTempDir(t)})

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

func TestIntegrationAdapterContractAttachedLauncherReportsDirectChildPIDBeforeWait(t *testing.T) {
	binDir := testutil.CanonicalTempDir(t)
	fake := filepath.Join(binDir, "agent")
	if err := testguard.WriteExecutable(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$$\"\nIFS= read -r release\n[ \"$release\" = released ]\n")); err != nil {
		t.Fatal(err)
	}
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		_ = release.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = release.Close(); _ = output.Close(); _ = childOutput.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := output.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	observed := 0
	var exits []agentexec.ProcessExit
	before := time.Now().UTC()

	err = (agentexec.AttachedLauncher{}).Run(ctx, agentexec.Command{Name: "agent", Command: fake}, agentexec.LaunchOptions{
		Dir: binDir, Stdin: input, Stdout: childOutput,
		OnExit: func(exit agentexec.ProcessExit) {
			exits = append(exits, exit)
			liveness, probeErr := agentexec.ProbePID(exit.PID)
			if probeErr != nil || liveness != agentexec.ProcessAbsent {
				t.Errorf("exit observer ran before child reaping: liveness=%s err=%v", liveness, probeErr)
			}
		},
		OnStart: func(pid int) error {
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil {
				return fmt.Errorf("read direct child identity before exit: %w", err)
			}
			childPID, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || childPID != pid || pid <= 0 {
				return fmt.Errorf("callback PID %d differs from child PID %q", pid, line)
			}
			observed = pid
			_, err = release.WriteString("released\n")
			return err
		},
	})

	if err != nil {
		t.Fatalf("launch and release direct child: %v", err)
	}
	if observed <= 0 {
		t.Fatal("child finished without its OnStart identity being observed")
	}
	if len(exits) != 1 || exits[0].PID != observed || exits[0].Code != 0 || exits[0].At.Before(before) || exits[0].At.After(time.Now().UTC()) {
		t.Fatalf("exit observations = %#v, want one observed zero exit of child %d", exits, observed)
	}
}

func TestIntegrationAdapterContractAttachedLauncherRunsExplicitlyRegisteredFake(t *testing.T) {
	binDir := testutil.CanonicalTempDir(t)
	marker := filepath.Join(testutil.CanonicalTempDir(t), "fake-ran")
	fake := filepath.Join(binDir, "pi")
	if err := testguard.WriteExecutable(fake, []byte("#!/bin/sh\nprintf invoked > "+marker+"\n")); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testguard.FakeAgentEnvKey("pi"), fake)

	err := (agentexec.AttachedLauncher{}).Run(context.Background(), agentexec.Command{
		Name:    "pi",
		Harness: "pi",
		Command: "pi",
	}, agentexec.LaunchOptions{Dir: testutil.CanonicalTempDir(t)})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fake was not invoked: %v", err)
	}
}

func TestIntegrationAdapterContractAttachedLauncherObservesReapedChildWhenPIDRecordingFails(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	probe := filepath.Join(dir, "pid-failure-probe")
	if err := testguard.WriteExecutable(probe, []byte("#!/bin/sh\nwhile :; do :; done\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pidFailure := errors.New("cannot persist direct child")
	var pid int
	var exits []agentexec.ProcessExit

	err := (agentexec.AttachedLauncher{}).Run(ctx, agentexec.Command{Name: "pid-failure-probe", Command: probe}, agentexec.LaunchOptions{
		Dir: dir,
		OnStart: func(child int) error {
			pid = child
			return pidFailure
		},
		OnExit: func(exit agentexec.ProcessExit) { exits = append(exits, exit) },
	})

	if !errors.Is(err, pidFailure) || agentexec.IsStartError(err) {
		t.Fatalf("Run error = %v, want retained PID-recording failure after startup", err)
	}
	if len(exits) != 1 || exits[0].PID != pid || exits[0].Code != -1 || exits[0].At.IsZero() {
		t.Fatalf("exit observations = %#v, want one observed killed-child result for %d", exits, pid)
	}
	liveness, probeErr := agentexec.ProbePID(pid)
	if probeErr != nil || liveness != agentexec.ProcessAbsent {
		t.Fatalf("child survived launch failure: liveness=%s err=%v", liveness, probeErr)
	}
}
