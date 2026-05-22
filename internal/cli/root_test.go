package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRootCommandHelp(t *testing.T) {
	is := assert.New(t)

	output, _ := executeCommand(t, []string{"--help"})

	is.Contains(output, "Orpheus")
	is.Contains(output, "Usage:")
	is.Contains(output, "--verbose")
}

func TestRootCommandDoesNotEmitDebugByDefault(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{})

	is.NotContains(stdout, "level=DEBUG")
	is.NotContains(stderr, "level=DEBUG")
}

func TestRootCommandVerboseEmitsDebugToStderr(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{"--verbose"})

	is.NotContains(stdout, "level=DEBUG")
	is.Contains(stderr, "level=DEBUG")
	is.Contains(stderr, "msg=\"rendering root help\"")
}
