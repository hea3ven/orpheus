// Package testguard provides test-process safety boundaries for external integrations.
package testguard

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	initialEnv = captureInitialEnvironment()
	usageRoots isolatedUsageRoots
)

var initialEnvironmentKeys = []string{
	"CODEX_HOME",
	"HOME",
	"PI_CODING_AGENT_DIR",
	"PI_CODING_AGENT_SESSION_DIR",
	"ORPHEUS_TEST_FAKE_AGENT_CODEX",
	"ORPHEUS_TEST_FAKE_AGENT_PI",
}

// IsTestProcess reports whether the current binary is a Go test binary.
func IsTestProcess() bool {
	return isTestProcess()
}

// EnvironmentChanged reports whether key differs from the environment inherited
// when this test binary started. Tests use t.Setenv to opt into fixture roots.
func EnvironmentChanged(key string) bool {
	if !IsTestProcess() {
		return false
	}
	value, ok := os.LookupEnv(key)
	initial, initiallySet := initialEnv[key]
	return ok != initiallySet || value != initial
}

// IsolatedUsageRoots returns temporary roots for session-log discovery in tests.
func IsolatedUsageRoots() UsageRoots {
	if !IsTestProcess() {
		return UsageRoots{}
	}
	usageRoots.once.Do(usageRoots.create)
	return usageRoots.roots
}

// UsageRoots identifies isolated test locations for supported harness state.
type UsageRoots struct {
	CodexHome    string
	Home         string
	PiDir        string
	PiSessionDir string
}

type isolatedUsageRoots struct {
	once  sync.Once
	roots UsageRoots
}

func (r *isolatedUsageRoots) create() {
	root, err := os.MkdirTemp("", "orpheus-test-agent-usage-")
	if err != nil {
		panic("create isolated test agent usage root: " + err.Error())
	}
	r.roots = UsageRoots{
		CodexHome:    filepath.Join(root, "codex"),
		Home:         filepath.Join(root, "home"),
		PiDir:        filepath.Join(root, "pi"),
		PiSessionDir: filepath.Join(root, "pi-sessions"),
	}
	for _, path := range []string{r.roots.CodexHome, r.roots.Home, r.roots.PiDir, r.roots.PiSessionDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			panic("create isolated test agent usage directory: " + err.Error())
		}
	}
}

// FakeAgentEnvKey returns the opt-in environment variable for a test fake.
func FakeAgentEnvKey(command string) string {
	return "ORPHEUS_TEST_FAKE_AGENT_" + normalizedCommand(command)
}

// FakeAgentPath returns the fake executable registered for command.
func FakeAgentPath(command string) string {
	key := FakeAgentEnvKey(command)
	if !IsTestProcess() || !EnvironmentChanged(key) {
		return ""
	}
	return strings.TrimSpace(os.Getenv(key))
}

func captureInitialEnvironment() map[string]string {
	values := make(map[string]string, len(initialEnvironmentKeys))
	for _, key := range initialEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	return values
}

func isTestProcess() bool {
	return hasTestRuntimeFlags(flag.CommandLine)
}

func hasTestRuntimeFlags(flags *flag.FlagSet) bool {
	return flags.Lookup("test.v") != nil
}

func normalizedCommand(command string) string {
	command = filepath.Base(strings.TrimSpace(command))
	command = strings.ToUpper(command)
	command = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, command)
	return command
}
