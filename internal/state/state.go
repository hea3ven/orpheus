// Package state resolves Orpheus state locations and provides generic file helpers.
package state

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// AppName is the directory name used under XDG config/data roots.
	AppName = "orpheus"

	directoryMode = 0o755
	fileMode      = 0o644
)

// Paths represents the resolved Orpheus configuration and data roots.
//
// Paths is intentionally generic: callers supply relative paths for the files
// and directories they own; this package does not encode any registry, Beads,
// Git, prompt, or CLI-specific layout.
type Paths struct {
	ConfigRoot string
	DataRoot   string
}

// ResolveOptions provides deterministic inputs for XDG path resolution.
type ResolveOptions struct {
	// HomeDir is used for XDG fallbacks when XDG_CONFIG_HOME or XDG_DATA_HOME is
	// not set. It must be absolute when required.
	HomeDir string

	// Env contains environment variables relevant to path resolution. A nil map
	// behaves like an empty environment.
	Env map[string]string
}

// NewPaths validates already-resolved Orpheus roots.
func NewPaths(configRoot, dataRoot string) (Paths, error) {
	if err := validateRoot("config root", configRoot); err != nil {
		return Paths{}, err
	}
	if err := validateRoot("data root", dataRoot); err != nil {
		return Paths{}, err
	}

	return Paths{
		ConfigRoot: filepath.Clean(configRoot),
		DataRoot:   filepath.Clean(dataRoot),
	}, nil
}

// Resolve determines Orpheus config/data roots from deterministic inputs.
//
// XDG_CONFIG_HOME and XDG_DATA_HOME are honored when set to absolute paths.
// Otherwise, Resolve falls back to $HOME/.config and $HOME/.local/share,
// respectively. The returned roots always include the "orpheus" application
// directory.
func Resolve(opts ResolveOptions) (Paths, error) {
	configBase, err := xdgBase(opts, "XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Paths{}, err
	}

	dataBase, err := xdgBase(opts, "XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return Paths{}, err
	}

	return NewPaths(filepath.Join(configBase, AppName), filepath.Join(dataBase, AppName))
}

// ResolveFromEnvironment resolves Orpheus roots from the current process
// environment and OS user home directory.
func ResolveFromEnvironment() (Paths, error) {
	env := map[string]string{}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	paths, resolveErr := Resolve(ResolveOptions{HomeDir: home, Env: env})
	if resolveErr != nil {
		if home == "" && err != nil {
			return Paths{}, fmt.Errorf("resolve Orpheus state paths: determine home directory: %w", err)
		}
		return Paths{}, fmt.Errorf("resolve Orpheus state paths: %w", resolveErr)
	}
	return paths, nil
}

// ConfigPath returns an absolute path under the Orpheus config root.
func (p Paths) ConfigPath(rel string) (string, error) {
	return p.join("config", p.ConfigRoot, rel)
}

// DataPath returns an absolute path under the Orpheus data root.
func (p Paths) DataPath(rel string) (string, error) {
	return p.join("data", p.DataRoot, rel)
}

// EnsureConfigDir creates a directory under the Orpheus config root and returns
// its absolute path.
func (p Paths) EnsureConfigDir(rel string) (string, error) {
	path, err := p.ConfigPath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, directoryMode); err != nil {
		return "", fmt.Errorf("create config directory %q (%s): %w", rel, path, err)
	}
	return path, nil
}

// EnsureDataDir creates a directory under the Orpheus data root and returns its
// absolute path.
func (p Paths) EnsureDataDir(rel string) (string, error) {
	path, err := p.DataPath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, directoryMode); err != nil {
		return "", fmt.Errorf("create data directory %q (%s): %w", rel, path, err)
	}
	return path, nil
}

// ReadConfigYAML reads a YAML file under the Orpheus config root into out.
func (p Paths) ReadConfigYAML(rel string, out any) error {
	path, err := p.ConfigPath(rel)
	if err != nil {
		return err
	}
	return readYAML("config", rel, path, out)
}

// WriteConfigYAML writes value as a YAML file under the Orpheus config root.
// Parent directories are created on demand and the target file is replaced via a
// complete temporary file in the same directory.
func (p Paths) WriteConfigYAML(rel string, value any) error {
	path, err := p.ConfigPath(rel)
	if err != nil {
		return err
	}
	return writeYAML("config", rel, path, value)
}

// ReadDataYAML reads a YAML file under the Orpheus data root into out.
func (p Paths) ReadDataYAML(rel string, out any) error {
	path, err := p.DataPath(rel)
	if err != nil {
		return err
	}
	return readYAML("data", rel, path, out)
}

// WriteDataYAML writes value as a YAML file under the Orpheus data root. Parent
// directories are created on demand and the target file is replaced via a
// complete temporary file in the same directory.
func (p Paths) WriteDataYAML(rel string, value any) error {
	path, err := p.DataPath(rel)
	if err != nil {
		return err
	}
	return writeYAML("data", rel, path, value)
}

func xdgBase(opts ResolveOptions, envKey, fallbackRel string) (string, error) {
	if value := opts.Env[envKey]; value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", envKey, value)
		}
		return filepath.Clean(value), nil
	}

	if opts.HomeDir == "" {
		return "", fmt.Errorf("home directory is required when %s is not set", envKey)
	}
	if !filepath.IsAbs(opts.HomeDir) {
		return "", fmt.Errorf("home directory must be an absolute path, got %q", opts.HomeDir)
	}

	return filepath.Join(filepath.Clean(opts.HomeDir), fallbackRel), nil
}

func (p Paths) join(kind, root, rel string) (string, error) {
	if err := validateRoot(kind+" root", root); err != nil {
		return "", err
	}

	cleanRel, err := cleanRelative(rel)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", kind, rel, err)
	}
	if cleanRel == "" {
		return filepath.Clean(root), nil
	}
	return filepath.Join(filepath.Clean(root), cleanRel), nil
}

func validateRoot(label, root string) error {
	if root == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%s must be an absolute path, got %q", label, root)
	}
	return nil
}

func cleanRelative(rel string) (string, error) {
	if rel == "" || rel == "." {
		return "", nil
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("path must be relative")
	}

	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("path must stay within the Orpheus state root")
	}
	if filepath.VolumeName(clean) != "" {
		return "", errors.New("path must not include a volume name")
	}

	return clean, nil
}

func readYAML(kind, rel, path string, out any) error {
	if out == nil {
		return fmt.Errorf("read %s YAML %q (%s): destination is nil", kind, rel, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s YAML %q (%s): file does not exist: %w", kind, rel, path, err)
		}
		return fmt.Errorf("read %s YAML %q (%s): %w", kind, rel, path, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("parse %s YAML %q (%s): %w", kind, rel, path, err)
	}

	return nil
}

func writeYAML(kind, rel, path string, value any) error {
	data, err := marshalYAML(value)
	if err != nil {
		return fmt.Errorf("encode %s YAML %q (%s): %w", kind, rel, path, err)
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, directoryMode); err != nil {
		return fmt.Errorf("create parent directory for %s YAML %q (%s): %w", kind, rel, path, err)
	}

	if err := writeFileAtomically(path, data, fileMode); err != nil {
		return fmt.Errorf("write %s YAML %q (%s): %w", kind, rel, path, err)
	}
	return nil
}

func marshalYAML(value any) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()

	return yaml.Marshal(value)
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
