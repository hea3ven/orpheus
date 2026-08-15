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
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type testRepoOption func(*testRepoConfig)

type testRepoConfig struct {
	withRemote bool
}

const immutableCLIHelperMode = 0o555

var (
	cliHelperFixtureRoot          string
	localOriginTestRepoTemplate   string
	localWorktreeTestRepoTemplate string
	normalTestRepoTemplate        string
	orpheusCLIHelperPath          string
)

func setupCLIHelperFixture() error {
	root, err := os.MkdirTemp("", "orpheus-cli-fixtures-*")
	if err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}
	if err := createSeededCLIRepositories(root); err != nil {
		_ = os.RemoveAll(root)
		return err
	}

	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("resolve test binary: %w", err)
	}
	helperPath := filepath.Join(root, "orpheus")
	script := fmt.Sprintf(`#!/bin/sh
GO_WANT_ORPHEUS_CLI_HELPER=1 exec %s -test.run=TestIntegrationOrpheusCLIHelperProcess -- "$@"
`, shellQuote(testBinary))
	if err := os.WriteFile(helperPath, []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("write orpheus helper: %w", err)
	}
	if err := os.Chmod(helperPath, immutableCLIHelperMode); err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("make orpheus helper immutable: %w", err)
	}
	if err := os.Chmod(root, immutableCLIHelperMode); err != nil {
		_ = os.Chmod(root, 0o700)
		_ = os.RemoveAll(root)
		return fmt.Errorf("make fixture directory immutable: %w", err)
	}
	cliHelperFixtureRoot = root
	orpheusCLIHelperPath = helperPath
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

	normalTestRepoTemplate = normalTemplate
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

func cleanupCLIHelperFixture() {
	if cliHelperFixtureRoot == "" {
		return
	}
	_ = os.Chmod(cliHelperFixtureRoot, 0o700)
	_ = os.RemoveAll(cliHelperFixtureRoot)
	cliHelperFixtureRoot = ""
	localOriginTestRepoTemplate = ""
	localWorktreeTestRepoTemplate = ""
	normalTestRepoTemplate = ""
	orpheusCLIHelperPath = ""
}

func isOrpheusCLIHelperProcess() bool {
	return os.Getenv("GO_WANT_ORPHEUS_CLI_HELPER") == "1"
}

func withoutRemote() testRepoOption {
	return func(config *testRepoConfig) {
		config.withRemote = false
	}
}

func newTestRepoPath(t *testing.T, opts ...testRepoOption) string {
	t.Helper()

	root := newTestState(t)

	config := testRepoConfig{withRemote: true}
	for _, opt := range opts {
		opt(&config)
	}

	return newTestRepoAt(t, root, filepath.Join("repos", "alpha"), config)
}

func newTestRepoAt(t *testing.T, root string, relativePath string, config testRepoConfig) string {
	t.Helper()
	must := require.New(t)

	repoPath := filepath.Join(root, relativePath)
	must.NoError(os.MkdirAll(repoPath, 0o755))
	initGitRepo(t, repoPath)
	if config.withRemote {
		name := filepath.Base(repoPath)
		runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/"+name+".git")
		runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
		runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return repoPath
}

func newSeededTestRepoAt(t *testing.T, root string, relativePath string, config testRepoConfig) string {
	t.Helper()

	repoPath := filepath.Join(root, relativePath)
	copySeededTestRepo(t, normalTestRepoTemplate, repoPath)
	if config.withRemote {
		name := filepath.Base(repoPath)
		runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/"+name+".git")
		runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
		runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return repoPath
}

func newTestRepoWithLocalOriginAt(t *testing.T, root string, relativePath string) string {
	t.Helper()

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

	root := canonicalTestPath(t, t.TempDir())
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

func configureTestGitUser(t *testing.T, repoPath string) {
	t.Helper()

	runGit(t, repoPath, "config", "user.name", "Orpheus Test")
	runGit(t, repoPath, "config", "user.email", "orpheus@example.com")
}

func initGitRepo(t *testing.T, repoPath string) {
	t.Helper()

	runGit(t, repoPath, "init")
	runGit(t, repoPath, "checkout", "-b", "main")
	configureTestGitUser(t, repoPath)
	runGit(t, repoPath, "commit", "--allow-empty", "-m", "initial")
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

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()

	canonicalPath, err := pathutil.CanonicalAbs(path)
	if err != nil {
		t.Fatalf("canonicalize test path %q: %v", path, err)
	}
	return canonicalPath
}

func executeCommand(t *testing.T, args []string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, args)
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

func executeCommandWithError(t *testing.T, args []string) (stdout string, stderr string, err error) {
	t.Helper()
	return executeCommandWithInputAndError(t, args, nil)
}

func executeCommandWithInput(t *testing.T, args []string, input string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithInputAndError(t, args, []byte(input))
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

func executeCommandWithInputAndError(t *testing.T, args []string, input []byte) (stdout string, stderr string, err error) {
	t.Helper()
	return executeCommandWithReaderAndError(t, args, bytes.NewBuffer(input))
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
	errOut := new(bytes.Buffer)
	cmd := newTestRootCommand(t, args, errOut)
	cmd.SetIn(input)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
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

func TestIntegrationOrpheusCLIHelperIsSharedAndImmutable(t *testing.T) {
	firstPath := withOrpheusCLIHelper(t)
	before, err := os.ReadFile(firstPath)
	require.NoError(t, err)

	secondPath := withOrpheusCLIHelper(t)
	after, err := os.ReadFile(secondPath)
	require.NoError(t, err)
	info, err := os.Stat(secondPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(immutableCLIHelperMode), info.Mode().Perm())
	require.Zero(t, info.Mode().Perm()&0o222)
	require.Equal(t, firstPath, secondPath)
	require.Equal(t, before, after)
}

func TestIntegrationSeededLocalOriginRepositoriesAreIndependent(t *testing.T) {
	root := newTestState(t)
	firstRepo := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "first"))
	secondRepo := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "second"))

	firstOrigin := strings.TrimSpace(runGit(t, firstRepo, "remote", "get-url", "origin"))
	secondOrigin := strings.TrimSpace(runGit(t, secondRepo, "remote", "get-url", "origin"))
	require.NotEqual(t, firstOrigin, secondOrigin)

	runGit(t, firstRepo, "checkout", "-b", "only-first")
	runGit(t, firstRepo, "commit", "--allow-empty", "-m", "only first")
	runGit(t, firstRepo, "push", "origin", "only-first")

	command := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/only-first")
	command.Dir = secondRepo
	require.Error(t, command.Run())
	require.Equal(t, "main\n", runGit(t, secondRepo, "branch", "--show-current"))
}

func withFakeBDInit(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
if [ -n "${FAKE_BD_LOCK_PATH-}" ] && [ ! -f "$FAKE_BD_LOCK_PATH" ]; then
  printf 'missing lock: %s\n' "$FAKE_BD_LOCK_PATH" >&2
  exit 43
fi
{
  pwd
  printf '%s\n' "$@"
  printf 'BD_NON_INTERACTIVE=%s\n' "${BD_NON_INTERACTIVE-unset}"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR-unset}"
} >> "$FAKE_BD_LOG"
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	setTestEnvironment(t, "FAKE_BD_LOG", logPath)
	prependTestPath(t, binDir)
	return logPath
}
