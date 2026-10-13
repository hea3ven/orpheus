//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestIntegrationAdapterContractComparisonCancellationKillsCollectorProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cancellation requires Unix")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & printf '%s\\n' ready; wait")
	configureProcessCancellation(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Stderr remains inherited by the sleeping descendant. Wait must not wait
	// for that pipe's 60-second lifetime or exhaust its five-second WaitDelay.
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected readiness %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			_ = cmd.Wait()
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		_ = cmd.Wait()
		t.Fatal("collector did not become ready")
	}

	started := time.Now()
	cancel()
	err = cmd.Wait()

	if err == nil || cmd.ProcessState == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("canceled collector was not promptly killed and reaped: %v, %v", cmd.ProcessState, err)
	}
}
