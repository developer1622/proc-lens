package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v3/common"
	"github.com/spf13/cobra"
)

/*
 * Note: This file governs the main entry points and global flag definitions for the ProcLens CLI.
 *
 * Caveat 1: The tool relies on host system metrics. Running inside containerized environments may
 * require configuring HOST_PROC and HOST_SYS environment variables to map host procfs and sysfs.
 *
 * Caveat 2: Exposing command line arguments via --expose-cmdline could leak sensitive credentials/secrets.
 * Ensure that output files are appropriately secured in production settings.
 */

type GlobalOptions struct {
	// OutputFormat defines the global output format (text or json).
	OutputFormat string

	// ExposeCmdline determines whether process command line arguments are shown verbatim.
	ExposeCmdline bool
}

var (
	// Version holds the application build version, injected at compile-time via ldflags.
	Version = "v1.0.0-dev"

	// GlobalOpts holds global configuration options.
	GlobalOpts GlobalOptions
)

// RootCmd represents the base command when called without any subcommands.
var RootCmd = &cobra.Command{
	Use:   "proc-lens",
	Short: "Universal Process Intelligence, Telemetry, and Workload Classifier",
	Long: `ProcLens is a production-grade observability and runtime analysis CLI tool.
It queries real-time resource telemetry on Windows, Linux, and macOS environments,
calculates process signatures, and classifies PIDs into high-level design (HLD) workload archetypes.
It also generates autonomous kernel optimization recommendations.
For usage details and available commands, run with the --help flag.`,
	Version: Version,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return RootCmd.Execute()
}

func init() {
	// Configure default slog structured logger
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Silence Cobra's default error logging and usage printing to let slog handle it gracefully.
	RootCmd.SilenceErrors = true
	RootCmd.SilenceUsage = true

	// Global persistent flags
	RootCmd.PersistentFlags().StringVarP(&GlobalOpts.OutputFormat, "format", "f", "text", "Output format. Select either 'text' or 'json' as per your requirements.")
	RootCmd.PersistentFlags().BoolVar(&GlobalOpts.ExposeCmdline, "expose-cmdline", false, "Expose raw process command-line arguments. WARNING: This may leak credentials/secrets. Use with caution.")

	// Validate host environment settings on startup
	cobra.OnInitialize(validateHostPaths)
}

// GetHostContext returns a context configured with HOST_PROC and HOST_SYS overrides for gopsutil.
// This is crucial for containerized runs to fetch host metrics correctly.
func GetHostContext() context.Context {
	ctx := context.Background()
	envMap := common.EnvMap{}
	if hp := os.Getenv("HOST_PROC"); hp != "" {
		envMap[common.HostProcEnvKey] = hp
	}
	if hs := os.Getenv("HOST_SYS"); hs != "" {
		envMap[common.HostSysEnvKey] = hs
	}
	if len(envMap) > 0 {
		ctx = context.WithValue(ctx, common.EnvKey, envMap)
	}
	return ctx
}

// validateHostPaths checks and logs host filesystem overrides for containerized execution.
// Warnings are logged if configured paths are inaccessible.
func validateHostPaths() {
	if runtime.GOOS != "linux" {
		return
	}

	if hp := os.Getenv("HOST_PROC"); hp != "" {
		slog.Info("Host procfs override configuration checked", "HOST_PROC", hp)
		if _, err := os.Stat(hp); err != nil {
			slog.Warn("Configured HOST_PROC path is not accessible, verify configuration", "path", hp, "error", err)
		}
	} else {
		slog.Info("Using default host procfs path (/proc)")
	}

	if hs := os.Getenv("HOST_SYS"); hs != "" {
		slog.Info("Host sysfs override configuration checked", "HOST_SYS", hs)
		if _, err := os.Stat(hs); err != nil {
			slog.Warn("Configured HOST_SYS path is not accessible, verify configuration", "path", hs, "error", err)
		}
	}
}

// RedactCmdline sanitizes the command line arguments unless explicitly overridden by exposeCmdline.
// By default, redaction is performed to prevent accidental secrets leakage.
func RedactCmdline(cmdline string, exposeCmdline bool) string {
	if exposeCmdline {
		return cmdline
	}

	fields := strings.Fields(cmdline)
	if len(fields) <= 1 {
		return cmdline
	}

	// Returns only the executable path/name, redacting subsequent parameters.
	return fields[0] + " [REDACTED]"
}

// Helper formatting colors
const (
	Reset  = "\033[0m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Blue   = "\033[34m"
	Purple = "\033[35m"
	Cyan   = "\033[36m"
	White  = "\033[37m"
	Bold   = "\033[1m"
	Dim    = "\033[2m"
)

// PrintJSONError prints an error in a structured JSON schema.
// The error message is properly escaped to avoid JSON parsing issues.
func PrintJSONError(err error) {
	fmt.Printf(`{"error": "%s"}`+"\n", strings.ReplaceAll(err.Error(), `"`, `\"`))
}

