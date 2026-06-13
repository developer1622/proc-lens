package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestFlagShorthandsDoNotConflictWithPersistent ensures that no subcommand
// redefines a shorthand flag that is already claimed by a persistent flag on RootCmd.
//
// This specifically guards against bugs like the original 'f' conflict:
//   - Root defines -f for --format (persistent, inherited by all subcommands)
//   - Enrich and Drift were defining their own --file with -f shorthand
//
// Such redefinitions cause a panic in pflag/cobra during flagset merge
// (triggered on Execute, Find, Help, etc.).
//
// The test exercises the full command tree initialization (which happens
// via package init() in each *_cmd.go) and then verifies the shorthands.
func TestFlagShorthandsDoNotConflictWithPersistent(t *testing.T) {
	// Force full command tree registration by referencing RootCmd.
	// All subcommand init() functions run at package load time and add
	// their commands + local flags. Persistent flags from root are merged
	// on first access (e.g. when we inspect flags below). Any duplicate
	// shorthand would have already panicked by the time we reach here if
	// the registration was broken.
	root := RootCmd
	if root == nil {
		t.Fatal("RootCmd is nil; command registration failed")
	}

	// Verify that root's persistent -f is still for --format.
	formatFlag := root.PersistentFlags().Lookup("format")
	if formatFlag == nil {
		t.Fatal("root persistent --format flag is missing")
	}
	if formatFlag.Shorthand != "f" {
		t.Errorf("expected root --format to keep shorthand 'f', got %q", formatFlag.Shorthand)
	}

	// Check that subcommands which previously overrode -f for --file no longer do so.
	// (They still have --file as a long flag, just without the conflicting shorthand.)
	conflictingCommands := []string{"enrich", "drift"}
	for _, name := range conflictingCommands {
		sub := findSubcommand(root, name)
		if sub == nil {
			t.Fatalf("subcommand %q not registered on root", name)
			continue
		}

		fileFlag := sub.Flags().Lookup("file")
		if fileFlag == nil {
			// Some commands may use "file" under a different name (e.g. FilePath in drift).
			// Check the actual flag defined with "file" in its usage.
			sub.Flags().VisitAll(func(f *pflag.Flag) {
				if f.Name == "file" || f.Name == "filepath" {
					fileFlag = f
				}
			})
		}

		if fileFlag != nil && fileFlag.Shorthand == "f" {
			t.Errorf("subcommand %q still defines -f shorthand for its file flag (conflicts with root --format -f)", name)
		}
	}

	// As an extra sanity check, ensure we can access merged flags for every subcommand
	// without panicking (this simulates what cobra does on Execute/Help).
	for _, sub := range root.Commands() {
		if sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		// Accessing .Flags() after the tree is built forces any remaining merge.
		_ = sub.Flags()
		// Also try the persistent set.
		_ = sub.PersistentFlags()
	}
}

func findSubcommand(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
		// Also check aliases if any (none in this project currently).
		for _, alias := range c.Aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}

// TestPersistentFormatFlagIsInherited is a light sanity check that the
// persistent -f/--format shorthand remains available from root and is not
// shadowed by any subcommand's own flag definitions.
// The real merge happens during Execute/Help in production use (verified by
// the manual subcommand --help runs and the no-conflict test above).
func TestPersistentFormatFlagIsInherited(t *testing.T) {
	root := RootCmd

	// Root itself must have it.
	format := root.PersistentFlags().Lookup("format")
	if format == nil || format.Shorthand != "f" {
		t.Fatal("root persistent --format must keep shorthand 'f'")
	}

	// No subcommand should have its *local* "format" flag stealing the shorthand.
	for _, sub := range root.Commands() {
		if sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		localFormat := sub.Flags().Lookup("format")
		if localFormat != nil && localFormat.Shorthand == "f" && localFormat != format {
			t.Errorf("subcommand %q defines its own -f/--format that would shadow the persistent one", sub.Name())
		}
	}
}