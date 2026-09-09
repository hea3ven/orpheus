//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	cleanupLocalBeadsFixture()
	cleanupCLIHelperFixture()
	os.Exit(code)
}
