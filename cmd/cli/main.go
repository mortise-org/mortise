package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "mortise",
		Short:        "Self-hosted Railway-style deploy platform",
		SilenceUsage: true,
	}
	cmd.AddCommand(newLoginCmd())
	cmd.AddCommand(newAppCmd())
	cmd.AddCommand(newDeployCmd())
	cmd.AddCommand(newLogsCmd())
	cmd.AddCommand(newStatusCmd())
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
