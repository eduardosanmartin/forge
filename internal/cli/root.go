// Package cli implements the forge command-line interface: root command,
// persistent flags, configuration bootstrapping, and logger wiring.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/logging"

	"github.com/spf13/cobra"
)

// App bundles the runtime dependencies derived from configuration that
// subcommands consume.
type App struct {
	Config *config.Config
	Logger *slog.Logger

	logFile *os.File
}

// Close releases resources owned by the App. It is safe to call multiple
// times.
func (a *App) Close() {
	if a.logFile != nil {
		_ = a.logFile.Close()
		a.logFile = nil
	}
}

type appContextKey struct{}

// withApp stores app in ctx for retrieval by subcommands via AppFromContext.
func withApp(ctx context.Context, app *App) context.Context {
	return context.WithValue(ctx, appContextKey{}, app)
}

// AppFromContext returns the App previously stored via withApp, if any.
func AppFromContext(ctx context.Context) (*App, bool) {
	app, ok := ctx.Value(appContextKey{}).(*App)
	return app, ok
}

// Execute runs the forge CLI and returns the resulting error, if any. Errors
// are already reported to stderr by cobra (SilenceErrors is false); callers
// typically translate a non-nil error into a non-zero exit status.
//
// It executes the RootCommand singleton so subcommands registered via init()
// hooks are part of the executable tree.
func Execute() error {
	defer closeApp(RootCommand)
	return RootCommand.ExecuteContext(context.Background())
}

// closeApp releases resources stashed on the root command's context after
// execution has finished, covering both success and failure paths.
func closeApp(root *cobra.Command) {
	if app, ok := AppFromContext(root.Context()); ok {
		app.Close()
	}
}

// NewRootCommand assembles the forge root command with its persistent flags
// and lifecycle hooks. Subcommands added in later units automatically inherit
// configuration loading and logger construction via PersistentPreRunE.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "forge",
		Short: "Local-first agentic development harness.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SilenceUsage = true
	root.SilenceErrors = false

	// Flag mistakes are usage errors: the entrypoint maps them to exit
	// code 2 (run.go defines the UsageError contract).
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &UsageError{Err: err}
	})

	root.PersistentFlags().String("config", "",
		"explicit project config path (overrides .forge/config.json)")
	root.PersistentFlags().String("log-level", "",
		"log level override applied on top of config (debug, info, warn, error)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		app, err := buildApp(cmd)
		if err != nil {
			return err
		}
		cmd.SetContext(withApp(cmd.Context(), app))
		return nil
	}

	root.AddCommand(newVersionCommand())
	return root
}

// RootCommand returns the root command for external registration.
var RootCommand = NewRootCommand()

// buildApp resolves configuration and logging for the invoked command:
// global config overlaid with the chosen project config, --log-level applied
// on top, validation, then logger construction.
func buildApp(cmd *cobra.Command) (*App, error) {
	flags := cmd.Flags()

	explicitConfig, err := flags.GetString("config")
	if err != nil {
		return nil, fmt.Errorf("read --config flag: %w", err)
	}
	levelOverride, err := flags.GetString("log-level")
	if err != nil {
		return nil, fmt.Errorf("read --log-level flag: %w", err)
	}

	globalPath, err := config.GlobalConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve global config path: %w", err)
	}
	projectPath, err := chooseProjectPath(explicitConfig)
	if err != nil {
		return nil, fmt.Errorf("resolve project config path: %w", err)
	}

	cfg, err := config.Load(globalPath, projectPath)
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if levelOverride != "" {
		cfg.Logging.Level = levelOverride
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	logger, logFile, err := logging.New(logging.Config{
		Level: cfg.Logging.Level,
		File:  cfg.Logging.File,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize logging: %w", err)
	}

	return &App{Config: cfg, Logger: logger, logFile: logFile}, nil
}

// chooseProjectPath honors an explicit --config value; otherwise it returns
// the default project-scoped path.
func chooseProjectPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return config.ProjectConfigPath()
}
