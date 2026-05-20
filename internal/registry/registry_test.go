package registry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestStoreLoadMissingRegistryIsEmpty(t *testing.T) {
	store := registry.NewStore(newTestPaths(t))

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load missing registry: %v", err)
	}
	if len(got.Repos) != 0 {
		t.Fatalf("repos = %#v, want empty", got.Repos)
	}
}

func TestStoreLoadEmptyRegistryFileIsEmpty(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "")
	store := registry.NewStore(paths)

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load empty registry: %v", err)
	}
	if len(got.Repos) != 0 {
		t.Fatalf("repos = %#v, want empty", got.Repos)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	paths := newTestPaths(t)
	store := registry.NewStore(paths)
	want := registry.Registry{Repos: []registry.Repo{{
		ID:   "orpheus",
		Name: "orpheus",
		Path: filepath.Join(paths.DataRoot, "..", "repos", "orpheus"),
	}}}
	want.Repos[0].Path = filepath.Clean(want.Repos[0].Path)

	if err := store.Save(want); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	registryPath, err := paths.DataPath("registry.yaml")
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}
	onDisk, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	if !strings.Contains(string(onDisk), "repos:") || !strings.Contains(string(onDisk), "id: orpheus") {
		t.Fatalf("registry file is not human-editable YAML: %s", onDisk)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	assertRepos(t, got.Repos, want.Repos)
}

func TestStoreLoadMalformedRegistry(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "repos: [unterminated\n")
	store := registry.NewStore(paths)

	_, err := store.Load()
	if err == nil {
		t.Fatal("load malformed registry succeeded, want error")
	}
	if !strings.Contains(err.Error(), "load repo registry") || !strings.Contains(err.Error(), "registry.yaml") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestStoreLoadRejectsUnknownRegistryFields(t *testing.T) {
	paths := newTestPaths(t)
	writeDataFile(t, paths, "registry.yaml", "repos: []\nunknown: true\n")
	store := registry.NewStore(paths)

	_, err := store.Load()
	if err == nil {
		t.Fatal("load registry with unknown field succeeded, want error")
	}
	if !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("error = %v, want unknown field validation", err)
	}
}

func TestRegistryAddRejectsDuplicateIDNameAndPath(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "alpha")
	existing := registry.Registry{Repos: []registry.Repo{{
		ID:   "alpha",
		Name: "Alpha",
		Path: basePath,
	}}}

	tests := []struct {
		name string
		repo registry.Repo
		want string
	}{
		{
			name: "id",
			repo: registry.Repo{ID: "alpha", Name: "Other", Path: filepath.Join(t.TempDir(), "other")},
			want: "duplicate repo id \"alpha\"",
		},
		{
			name: "name",
			repo: registry.Repo{ID: "other", Name: "Alpha", Path: filepath.Join(t.TempDir(), "other")},
			want: "duplicate repo name \"Alpha\"",
		},
		{
			name: "path",
			repo: registry.Repo{ID: "other", Name: "Other", Path: filepath.Join(basePath, "..", "alpha")},
			want: "duplicate repo path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := existing
			err := reg.Add(tt.repo)
			if err == nil {
				t.Fatal("add duplicate repo succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			assertRepos(t, reg.Repos, existing.Repos)
		})
	}
}

func TestNewRepoFromPathDerivesIdentityAndNormalizesPath(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "..", filepath.Base(root), "my-repo", ".")

	got, err := registry.NewRepoFromPath(input)
	if err != nil {
		t.Fatalf("new repo from path: %v", err)
	}

	wantPath := filepath.Join(root, "my-repo")
	if got.ID != "my-repo" || got.Name != "my-repo" || got.Path != wantPath {
		t.Fatalf("repo = %#v, want id/name my-repo path %q", got, wantPath)
	}
}

func TestNewRepoFromPathRejectsBlankAndRoot(t *testing.T) {
	tests := []string{"", "   ", filepath.VolumeName(filepath.Clean(string(os.PathSeparator))) + string(os.PathSeparator)}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := registry.NewRepoFromPath(input)
			if err == nil {
				t.Fatal("new repo from invalid path succeeded, want error")
			}
		})
	}
}

func TestStoreSaveRejectsInvalidRegistry(t *testing.T) {
	store := registry.NewStore(newTestPaths(t))

	err := store.Save(registry.Registry{Repos: []registry.Repo{{ID: "missing-path", Name: "missing-path"}}})
	if err == nil {
		t.Fatal("save invalid registry succeeded, want error")
	}
	if !strings.Contains(err.Error(), "save repo registry") || !strings.Contains(err.Error(), "repo path is required") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func newTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}

func writeDataFile(t *testing.T, paths state.Paths, rel string, content string) {
	t.Helper()

	path, err := paths.DataPath(rel)
	if err != nil {
		t.Fatalf("data path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir data file parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
}

func assertRepos(t *testing.T, got []registry.Repo, want []registry.Repo) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("repo count = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("repo[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
