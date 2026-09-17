//go:build integration

package pullrequest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGHProviderPublicationTranslatesIdentityAndBody(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	binary, argsLog, bodyLog := filepath.Join(root, "gh"), filepath.Join(root, "args"), filepath.Join(root, "body")
	// Each argument has its own line; the multiline body must travel on stdin.
	script := "#!/bin/sh\n[ \"$GH_SCOPE\" = isolated ] || exit 17\npwd > " + shellQuote(argsLog) + "\nprintf '%s\\n' \"$@\" >> " + shellQuote(argsLog) + "\ncase \"$2\" in\nlist) printf '[{\"url\":\"https://github.test/org/repo/pull/7\"}]';;\ncreate) cat > " + shellQuote(bodyLog) + "; printf 'Created\\nhttps://github.test/org/repo/pull/8\\n';;\n*) exit 66;;\nesac\n"
	require.NoError(t, testguard.WriteExecutable(binary, []byte(script)))
	provider := pullrequest.GHProvider{Binary: binary, Environment: []string{"GH_SCOPE=isolated"}}

	found, ok, err := provider.FindOpenByBranch(context.Background(), pullrequest.FindOpenByBranchRequest{RepositoryPath: root, HeadBranch: "feature/task", BaseBranch: "release"})

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "https://github.test/org/repo/pull/7", found.URL)
	logged, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.Equal(t, root+"\npr\nlist\n--state\nopen\n--head\nfeature/task\n--base\nrelease\n--json\nurl\n--limit\n1\n", string(logged))
	body := "## Details\n\nLiteral `code` and $HOME.\n"
	created, err := provider.Create(context.Background(), pullrequest.CreateRequest{RepositoryPath: root, HeadBranch: "feature/task", BaseBranch: "release", Title: "Keep title intact", Body: body})
	require.NoError(t, err)
	assert.Equal(t, "https://github.test/org/repo/pull/8", created.URL)
	logged, err = os.ReadFile(argsLog)
	require.NoError(t, err)
	assert.Equal(t, root+"\npr\ncreate\n--base\nrelease\n--head\nfeature/task\n--title\nKeep title intact\n--body-file\n-\n", string(logged))
	stdin, err := os.ReadFile(bodyLog)
	require.NoError(t, err)
	assert.Equal(t, body, string(stdin))
}

func TestIntegrationGHProviderPublicationOutputAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, output, wantError string
		exit                    int
		create                  bool
	}{
		{name: "no existing PR", output: "[]"},
		{name: "malformed list", output: "{", wantError: "not JSON"},
		{name: "missing listed URL", output: "[{}]", wantError: "valid PR URL"},
		{name: "malformed listed URL", output: `[{"url":"not-a-url"}]`, wantError: "valid PR URL"},
		{name: "lookup authentication", output: "gh auth login required", exit: 1, wantError: "gh authentication failed or is missing"},
		{name: "lookup repository unavailable", output: "Could not resolve to a Repository with the name 'org/alpha'", exit: 1, wantError: "could not be resolved by gh"},
		{name: "create authentication", create: true, output: "gh auth login required", exit: 1, wantError: "gh authentication failed or is missing"},
		{name: "create missing URL", create: true, output: "created", wantError: "valid PR URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGH(t, tc.output, tc.exit)
			root := testutil.CanonicalTempDir(t)
			var err error
			if tc.create {
				_, err = (pullrequest.GHProvider{}).Create(context.Background(), pullrequest.CreateRequest{RepositoryPath: root, HeadBranch: "feature", BaseBranch: "main", Title: "Title", Body: "Body"})
			} else {
				_, found, findErr := (pullrequest.GHProvider{}).FindOpenByBranch(context.Background(), pullrequest.FindOpenByBranchRequest{RepositoryPath: root, HeadBranch: "feature", BaseBranch: "main"})
				err = findErr
				assert.False(t, found)
			}
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}

func TestIntegrationGHProviderStatusClassifiesRepositoryAndAuthenticationFailures(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{"gh auth login required", "gh authentication failed or is missing"},
		{"Could not resolve to a Repository with the name 'org/alpha'", "could not be resolved by gh"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			log := installFakeGH(t, tc.output, 1)
			_, err := (pullrequest.GHProvider{}).StatusByURL(context.Background(), pullrequest.StatusByURLRequest{URL: "https://github.test/org/alpha/pull/42"})
			require.ErrorContains(t, err, tc.want)
			logged, err := os.ReadFile(log)
			require.NoError(t, err)
			assert.Equal(t, "pr view https://github.test/org/alpha/pull/42 --json url,state,mergedAt", strings.TrimSpace(string(logged)))
		})
	}
}
