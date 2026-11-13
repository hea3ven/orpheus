package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// fileLoggingConfig decodes only its section of the shared global config.
// Sibling configuration remains owned and validated by its existing consumers.
type fileLoggingConfig struct {
	Enabled bool
	Level   slog.Level
}

func (c *fileLoggingConfig) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Logging struct {
			File struct {
				Enabled bool    `yaml:"enabled"`
				Level   *string `yaml:"level"`
			} `yaml:"file"`
		} `yaml:"logging"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	c.Enabled = raw.Logging.File.Enabled
	level := "debug"
	if raw.Logging.File.Level != nil {
		level = *raw.Logging.File.Level
	}
	switch level {
	case "debug":
		c.Level = slog.LevelDebug
	case "info":
		c.Level = slog.LevelInfo
	case "warn":
		c.Level = slog.LevelWarn
	case "error":
		c.Level = slog.LevelError
	default:
		return fmt.Errorf("logging.file.level %q is unsupported; choose debug, info, warn, or error", level)
	}
	return nil
}

func (o *rootOptions) configureLogging(command *cobra.Command) error {
	o.logger = logging.New(command.ErrOrStderr(), logging.Config{Verbose: o.verbose})
	// Isolated evaluations deliberately work without normal invocation state.
	if skipsInvocation(command) {
		return nil
	}
	var paths state.Paths
	var err error
	if o.invocationDeps != nil {
		paths = o.invocationDeps.paths
	} else {
		paths, err = o.commandOptions.resolvePaths()
	}
	if err != nil {
		return err
	}
	o.commandOptions.Paths = &paths
	file := fileLoggingConfig{Level: slog.LevelDebug}
	if err := paths.ReadConfigYAML("config.yaml", &file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load logging configuration: %w", err)
	}
	config := logging.Config{Verbose: o.verbose, FileLevel: file.Level}
	if file.Enabled {
		root, err := o.loggingStateRoot()
		if err != nil {
			_, _ = fmt.Fprintf(command.ErrOrStderr(), "Warning: file logging disabled: %v\n", err)
		} else {
			config.FilePath = filepath.Join(root, "orpheus.log")
		}
	}
	o.logger = logging.New(command.ErrOrStderr(), config)
	return nil
}

func (o *rootOptions) loggingStateRoot() (string, error) {
	env := o.commandOptions.Environment
	if o.invocationDeps != nil {
		env = o.invocationDeps.environment
	}
	if env != nil {
		return state.ResolveStateRoot(state.ResolveOptions{HomeDir: env["HOME"], Env: env})
	}
	home, _ := os.UserHomeDir()
	return state.ResolveStateRoot(state.ResolveOptions{
		HomeDir: home,
		Env:     map[string]string{"XDG_STATE_HOME": os.Getenv("XDG_STATE_HOME")},
	})
}
