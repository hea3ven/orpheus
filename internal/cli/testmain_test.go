//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if isOrpheusCLIHelperProcess() {
		os.Exit(m.Run())
	}
	if err := setupCLIHelperFixture(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "setup CLI helper fixture: %v\n", err)
		os.Exit(2)
	}
	code := m.Run()
	cleanupLocalBeadsFixture()
	cleanupCLIHelperFixture()
	os.Exit(code)
}
