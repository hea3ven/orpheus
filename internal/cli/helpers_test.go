//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// writeTestExecutable makes a fixture visible only after its complete content and
// executable mode are in place. This prevents an overlapping command from trying
// to execute a script while it is being replaced.
func writeTestExecutable(path string, content []byte) error {
	return testguard.WriteExecutable(path, content)
}

var (
	cliIntegrationFixtureOnce     sync.Once
	cliIntegrationFixtureErr      error
	cliIntegrationFixtureRoot     string
	localOriginTestRepoTemplate   string
	localWorktreeTestRepoTemplate string
)

func requireCLIIntegrationFixture(t *testing.T) {
	t.Helper()

	cliIntegrationFixtureOnce.Do(func() {
		cliIntegrationFixtureErr = setupCLIIntegrationFixture()
	})
	if cliIntegrationFixtureErr != nil {
		t.Fatalf("setup CLI integration fixture: %v", cliIntegrationFixtureErr)
	}
}

func setupCLIIntegrationFixture() error {
	root, err := os.MkdirTemp("", "orpheus-cli-fixtures-*")
	if err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}
	if err := createSeededCLIRepositories(root); err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	cliIntegrationFixtureRoot = root
	return nil
}

func createSeededCLIRepositories(root string) error {
	normalTemplate := filepath.Join(root, "normal-repository")
	if err := runFixtureCommand("", "git", "init", "--initial-branch=main", normalTemplate); err != nil {
		return err
	}
	if err := runFixtureCommand(normalTemplate, "git", "config", "user.name", "Orpheus Test"); err != nil {
		return err
	}
	if err := runFixtureCommand(normalTemplate, "git", "config", "user.email", "orpheus@example.com"); err != nil {
		return err
	}
	if err := runFixtureCommand(normalTemplate, "git", "commit", "--allow-empty", "-m", "initial"); err != nil {
		return err
	}

	originTemplate := filepath.Join(root, "local-origin.git")
	if err := runFixtureCommand("", "git", "init", "--bare", "--initial-branch=main", originTemplate); err != nil {
		return err
	}
	worktreeTemplate := filepath.Join(root, "local-worktree")
	if err := copyTestTree(normalTemplate, worktreeTemplate); err != nil {
		return fmt.Errorf("copy local worktree template: %w", err)
	}
	if err := runFixtureCommand(worktreeTemplate, "git", "remote", "add", "origin", originTemplate); err != nil {
		return err
	}
	if err := runFixtureCommand(worktreeTemplate, "git", "push", "--set-upstream", "origin", "main"); err != nil {
		return err
	}
	if err := runFixtureCommand(worktreeTemplate, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		return err
	}

	localOriginTestRepoTemplate = originTemplate
	localWorktreeTestRepoTemplate = worktreeTemplate
	return nil
}

func runFixtureCommand(dir string, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v in %s: %w\n%s", name, args, dir, err, output)
	}
	return nil
}

func copyTestTree(source string, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyTestFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported fixture entry %s (%s)", path, info.Mode())
		}
	})
}

func copyTestFile(source string, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func cleanupCLIIntegrationFixture() {
	if cliIntegrationFixtureRoot == "" {
		return
	}
	_ = os.RemoveAll(cliIntegrationFixtureRoot)
	cliIntegrationFixtureRoot = ""
	localOriginTestRepoTemplate = ""
	localWorktreeTestRepoTemplate = ""
}

func newTestRepoWithLocalOriginAt(t *testing.T, root string, relativePath string) string {
	t.Helper()
	requireCLIIntegrationFixture(t)

	originPath := filepath.Join(root, "origins", filepath.Base(relativePath)+".git")
	copySeededTestRepo(t, localOriginTestRepoTemplate, originPath)

	repoPath := filepath.Join(root, relativePath)
	copySeededTestRepo(t, localWorktreeTestRepoTemplate, repoPath)
	setSeededLocalOrigin(t, repoPath, originPath)
	return repoPath
}

func copySeededTestRepo(t *testing.T, source string, destination string) {
	t.Helper()
	if source == "" {
		t.Fatal("CLI test fixtures are not initialized")
	}
	if err := copyTestTree(source, destination); err != nil {
		t.Fatalf("copy seeded repository %s to %s: %v", source, destination, err)
	}
}

func setSeededLocalOrigin(t *testing.T, repoPath string, originPath string) {
	t.Helper()

	configPath := filepath.Join(repoPath, ".git", "config")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read seeded repository config: %v", err)
	}
	updated := strings.Replace(string(config), localOriginTestRepoTemplate, originPath, 1)
	if updated == string(config) {
		t.Fatalf("seeded repository config does not contain origin %q", localOriginTestRepoTemplate)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat seeded repository config: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(updated), info.Mode().Perm()); err != nil {
		t.Fatalf("set seeded repository origin: %v", err)
	}
}

type testInvocation struct {
	root        string
	paths       state.Paths
	environment map[string]string
}

var testInvocations sync.Map // map[string]*testInvocation

func newTestState(t *testing.T) string {
	t.Helper()

	return testInvocationFor(t).root
}

func testInvocationFor(t *testing.T) *testInvocation {
	t.Helper()

	key := t.Name()
	if existing, ok := testInvocations.Load(key); ok {
		return existing.(*testInvocation)
	}

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(
		filepath.Join(root, "xdg-config", state.AppName),
		filepath.Join(root, "xdg-data", state.AppName),
	)
	if err != nil {
		t.Fatalf("create test state paths: %v", err)
	}
	environment := invocationEnvironmentSnapshot()
	for _, name := range orpheusAgentEnvironmentNames {
		environment[name] = ""
	}
	fixture := &testInvocation{
		root:        root,
		paths:       paths,
		environment: environment,
	}
	actual, loaded := testInvocations.LoadOrStore(key, fixture)
	if loaded {
		return actual.(*testInvocation)
	}
	t.Cleanup(func() { testInvocations.Delete(key) })
	return fixture
}

var orpheusAgentEnvironmentNames = []string{
	"ORPHEUS_REPO_ID", "ORPHEUS_TASK_ID", "ORPHEUS_WORKTREE", "ORPHEUS_BRANCH",
	"ORPHEUS_AGENT_PROMPT", "ORPHEUS_AGENT_PURPOSE", "ORPHEUS_CONFLICT_FILES",
	"ORPHEUS_REVIEW_ATTEMPT", "ORPHEUS_REVIEW_STEP",
}

func setTestEnvironment(t *testing.T, name string, value string) {
	t.Helper()

	testInvocationFor(t).environment[name] = value
}

func prependTestPath(t *testing.T, directory string) {
	t.Helper()

	fixture := testInvocationFor(t)
	fixture.environment["PATH"] = directory + string(os.PathListSeparator) + fixture.environment["PATH"]
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func canonicalFixturePath(t *testing.T, path string) string {
	t.Helper()

	canonicalPath, err := pathutil.CanonicalAbs(path)
	if err != nil {
		t.Fatalf("canonicalize test path %q: %v", path, err)
	}
	return canonicalPath
}

func executeCommandWithScriptedInput(t *testing.T, args []string, input ...string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithReaderAndError(t, args, &scriptedInput{chunks: input})
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

type scriptedInput struct {
	chunks []string
	index  int
	offset int
}

func (r *scriptedInput) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	if chunk == "" {
		r.index++
		r.offset = 0
		return 0, io.EOF
	}
	n := copy(p, chunk[r.offset:])
	r.offset += n
	if r.offset >= len(chunk) {
		r.index++
		r.offset = 0
	}
	return n, nil
}

func executeCommandWithReaderAndError(t *testing.T, args []string, input io.Reader) (stdout string, stderr string, err error) {
	t.Helper()

	out := new(bytes.Buffer)
	errOut := new(synchronizedBuffer)
	cmd := newTestRootCommand(t, args, errOut)
	cmd.SetIn(input)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func newTestRootCommand(t *testing.T, args []string, errOut io.Writer) *cobra.Command {
	t.Helper()

	fixture := testInvocationFor(t)
	logger := logging.Discard()
	if containsArgument(args, "--verbose") || containsArgument(args, "-v") {
		logger = logging.New(errOut, logging.Config{Verbose: true})
	}
	deps := newInvocationDependenciesWithPaths(fixture.paths, logger, fixture.environment)
	return newRootCommand(&rootOptions{logger: logger, invocationDeps: deps})
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func currentTestPaths(t *testing.T) state.Paths {
	t.Helper()

	return testInvocationFor(t).paths
}
