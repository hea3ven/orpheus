package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	gitadapter "github.com/hea3ven/orpheus/internal/git"
)

func TestComparisonEnvironmentOverridesOperatorDoltRoot(t *testing.T) {
	t.Setenv("DOLT_ROOT_PATH", "/fixture/operator-dolt-root")

	environment := comparisonEnvironment("/fixture/comparison", "base")

	var roots []string
	for _, entry := range environment {
		if root, found := strings.CutPrefix(entry, "DOLT_ROOT_PATH="); found {
			roots = append(roots, root)
		}
	}
	if !reflect.DeepEqual(roots, []string{"/fixture/comparison/home"}) {
		t.Fatalf("Dolt roots = %v, want only the isolated comparison home", roots)
	}
}

func TestComparisonEnvironmentIsolatesTestBinaryCachesByRevision(t *testing.T) {
	t.Setenv(testBinaryCacheEnvironment, "/fixture/operator-cache")

	for _, name := range []string{"base", "merge"} {
		environment := comparisonEnvironment("/fixture/comparison", name)

		var caches []string
		for _, entry := range environment {
			if cache, found := strings.CutPrefix(entry, testBinaryCacheEnvironment+"="); found {
				caches = append(caches, cache)
			}
		}
		want := []string{"/fixture/comparison/test-binaries-" + name}
		if !reflect.DeepEqual(caches, want) {
			t.Fatalf("%s test binary caches = %v, want %v", name, caches, want)
		}
	}
}

func TestComparisonEnvironmentRemovesInheritedGitConfiguration(t *testing.T) {
	inherited := map[string]string{
		"GIT_CONFIG":            "/fixture/operator-config",
		"GIT_CONFIG_SYSTEM":     "/fixture/operator-system-config",
		"GIT_CONFIG_GLOBAL":     "/fixture/operator-global-config",
		"GIT_CONFIG_NOSYSTEM":   "0",
		"GIT_CONFIG_COUNT":      "1",
		"GIT_CONFIG_KEY_0":      "core.hooksPath",
		"GIT_CONFIG_VALUE_0":    "/fixture/operator-hooks",
		"GIT_CONFIG_PARAMETERS": "'core.hooksPath=/fixture/operator-hooks'",
	}
	for key, value := range inherited {
		t.Setenv(key, value)
	}

	for _, name := range []string{"setup", "collector", "base", "merge"} {
		environment := environmentValues(comparisonEnvironment("/fixture/comparison", name))

		configuration := make(map[string]string)
		for key := range inherited {
			if value, found := environment[key]; found {
				configuration[key] = value
			}
		}
		want := map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"}
		if !reflect.DeepEqual(configuration, want) {
			t.Fatalf("%s Git configuration = %v, want only %v", name, configuration, want)
		}
	}
}

type identityRunner struct {
	head   string
	status string
	fail   bool
	calls  []gitadapter.Command
}

func (runner *identityRunner) Run(_ context.Context, command gitadapter.Command) (gitadapter.CommandResult, error) {
	runner.calls = append(runner.calls, command)
	if runner.fail {
		return gitadapter.CommandResult{Stderr: "fixture failure"}, errors.New("git failed")
	}
	if command.Args[0] == "rev-parse" {
		return gitadapter.CommandResult{Stdout: runner.head + "\n"}, nil
	}
	return gitadapter.CommandResult{Stdout: runner.status}, nil
}

func TestSourceIdentityValidationIsIndependentOfEnvironmentCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, expected, head, status string
		processFailed, wantErr       bool
		calls                        int
	}{
		{name: "clean exact identity", expected: fixtureBase, head: fixtureBase, calls: 2},
		{name: "wrong identity", expected: fixtureBase, head: fixtureMerge, wantErr: true, calls: 1},
		{name: "short identity", expected: fixtureBase[:12], head: fixtureBase, wantErr: true},
		{name: "dirty source", expected: fixtureBase, head: fixtureBase, status: " M source.go\n", wantErr: true, calls: 2},
		{name: "untracked source", expected: fixtureBase, head: fixtureBase, status: "?? extra_test.go\n", wantErr: true, calls: 2},
		{name: "failed identity probe", expected: fixtureBase, processFailed: true, wantErr: true, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &identityRunner{head: test.head, status: test.status, fail: test.processFailed}

			identity, err := verifyCheckoutWithRunner("/fixture/checkout", test.expected, runner)

			if (err != nil) != test.wantErr || len(runner.calls) != test.calls {
				t.Fatalf("identity = %#v, %v, %d commands; want error %t, %d commands", identity, err, len(runner.calls), test.wantErr, test.calls)
			}
			if err == nil && (!identity.Clean || identity.Revision != test.expected) {
				t.Fatalf("unverified source identity: %#v", identity)
			}
			if len(runner.calls) == 2 && !reflect.DeepEqual(runner.calls[1], gitadapter.Command{Directory: "/fixture/checkout", Args: []string{"status", "--porcelain", "--untracked-files=all"}}) {
				t.Fatalf("cleanliness probe = %#v", runner.calls[1])
			}
		})
	}
}

func TestPrivateGitEnvironmentCannotUseOperatorRepositoryRouting(t *testing.T) {
	input := []string{"PATH=/fixture/bin", "GIT_DIR=/fixture/operator", "GIT_WORK_TREE=/fixture/operator", "GIT_INDEX_FILE=/fixture/operator/index", "GIT_OBJECT_DIRECTORY=/fixture/operator/objects", "GIT_COMMON_DIR=/fixture/operator", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/fixture/operator/objects", "GIT_CONFIG_GLOBAL=/fixture/ignores"}

	got := gitRoutingEnvironment(input)

	want := []string{"PATH=/fixture/bin", "GIT_CONFIG_GLOBAL=/fixture/ignores"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Git environment = %v, want %v", got, want)
	}
}
