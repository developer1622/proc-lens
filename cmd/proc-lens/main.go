package main

import (
	"log/slog"
	"os"
	"github.com/developer1622/proc-lens/pkg/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		slog.Error("Application terminated with error", "error", err)
		os.Exit(1)
	}
}

