package testguard_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/testguard"
)

func TestIsTestProcess(t *testing.T) {
	if !testguard.IsTestProcess() {
		t.Fatal("IsTestProcess() = false, want true in Go test binary")
	}
}
