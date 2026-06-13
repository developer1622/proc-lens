package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/optimizer"

	"github.com/spf13/cobra"
)

/*
 * Note: This file contains the run command which executes a subprocess, profiles its telemetry,
 * and prints predictions before terminating it.
 *
 * Caveat 1: Executing arbitrary commands in a privileged/host-telemetry context is a security risk.
 * Pass the --allow-run flag explicitly to proceed with the execution.
 *
 * Caveat 2: If the subprocess exits before the second telemetry sample is taken, the rate-based profile
 * calculation cannot be completed. Select a profiling duration (--duration) shorter than the run-time
 * of the program.
 */

type RunOptions struct {
	CmdStr        string
	Duration      time.Duration
	AllowRun      bool
	OutputFormat  string
	ExposeCmdline bool
}

var runOpts RunOptions

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Launch a custom program, profile it, and predict its HLD category",
	Long:  `Executes a target binary as a subprocess, profiles its telemetry, and prints the workload classification before stopping it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := runOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		opts.ExposeCmdline = GlobalOpts.ExposeCmdline
		return RunSubprocessCmd(ctx, &opts)
	},
}

func RunSubprocessCmd(ctx context.Context, opts *RunOptions) error {
	if hp := os.Getenv("HOST_PROC"); hp != "" && os.Getuid() == 0 {
		err := fmt.Errorf("security gate blocked execution: refusing to execute run command in a host-privileged context (UID 0 and HOST_PROC active). Use a non-privileged binary instead")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sSecurity Error: %v%s\n", Red, err, Reset)
		}
		return err
	}

	if !opts.AllowRun {
		err := fmt.Errorf("security validation failed: running arbitrary commands in host-telemetry contexts can expose your node to command execution and data exfiltration. Pass --allow-run explicitly to proceed with the execution")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sSecurity Warning: %v%s\n", Red, err, Reset)
		}
		return err
	}

	// Security Warning for Privilege Context (Concern 5)
	if os.Getuid() == 0 {
		slog.Warn("WARNING: proc-lens is running as UID 0 (root) and --allow-run is active. Launching a subprocess under root privileges is extremely dangerous. Ensure the binary to run is trusted and verified.")
	}

	if opts.CmdStr == "" {
		err := fmt.Errorf("invalid command. Provide a command to execute using the --cmd flag")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	if opts.Duration <= 0 {
		err := fmt.Errorf("invalid duration: %v. Provide a positive profiling duration", opts.Duration)
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	fields := strings.Fields(opts.CmdStr)
	if len(fields) == 0 {
		err := fmt.Errorf("empty command specified, provide a valid command")
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	commandName := fields[0]
	commandArgs := fields[1:]

	subprocess := exec.Command(commandName, commandArgs...)
	subprocess.Stdout = os.Stdout
	subprocess.Stderr = os.Stderr

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	if opts.OutputFormat != "json" {
		fmt.Printf("%sLaunching program: %s%s\n", Bold, opts.CmdStr, Reset)
	}

	err := subprocess.Start()
	if err != nil {
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sFailed to start process: %v. Check executable path and permissions.%s\n", Red, err, Reset)
		}
		return err
	}

	pid := subprocess.Process.Pid

	if opts.OutputFormat != "json" {
		fmt.Printf("Subprocess started successfully (PID: %d). Profiling resource consumption...\n", pid)
	}

	// Capture the first stats sample immediately
	s1, err := collector.GetRawStats(subCtx, pid, commandName, opts.CmdStr)
	if err != nil {
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sFailed to capture initial telemetry sample: %v%s\n", Red, err, Reset)
		}
		_ = subprocess.Process.Kill()
		_ = subprocess.Wait()
		return err
	}

	// Profiling sleep window
	select {
	case <-time.After(opts.Duration):
		// Proceed
	case <-subCtx.Done():
		if opts.OutputFormat != "json" {
			fmt.Printf("\n%sSubprocess profiling has been interrupted.%s\n", Yellow, Reset)
		}
		_ = subprocess.Process.Kill()
		_ = subprocess.Wait()
		return subCtx.Err()
	}

	// Capture the second stats sample
	s2, err := collector.GetRawStats(subCtx, pid, commandName, opts.CmdStr)
	if err != nil {
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sFailed to capture final telemetry sample: %v. The process may have exited too quickly. Configure a shorter duration.%s\n", Red, err, Reset)
		}
		_ = subprocess.Process.Kill()
		_ = subprocess.Wait()
		return err
	}

	stats := collector.CalculateRateStats(s1, s2)
	pred := classifier.Predict(stats)
	pred.Cmdline = RedactCmdline(pred.Cmdline, opts.ExposeCmdline)
	pred.Telemetry.Cmdline = RedactCmdline(pred.Telemetry.Cmdline, opts.ExposeCmdline)
	optimizer.Optimize(subCtx, &pred)

	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(pred, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return err
		}
		fmt.Println(string(bz))
	} else {
		printPredictionReport(pred)
	}

	// Clean up process if still running
	if subprocess.ProcessState == nil {
		if opts.OutputFormat != "json" {
			fmt.Printf("%sTerminating profiled process PID %d...%s\n", Dim, pid, Reset)
		}
		_ = subprocess.Process.Kill()
		_ = subprocess.Wait()
	}
	return nil
}

func init() {
	runCmd.Flags().StringVarP(&runOpts.CmdStr, "cmd", "x", "", "Command to execute and profile (required)")
	runCmd.Flags().DurationVarP(&runOpts.Duration, "duration", "d", 3*time.Second, "Profiling window duration (e.g. 1s, 2s, 3s)")
	runCmd.Flags().BoolVar(&runOpts.AllowRun, "allow-run", false, "Explicitly allow launching arbitrary command subprocesses in privileged host contexts (WARNING: security risk)")
	
	RootCmd.AddCommand(runCmd)
}

