package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mortise",
		Short: "Self-hosted Railway-style deploy platform",
		Long: `mortise — self-hosted Railway-style deploy platform.

Apps live inside projects. The CLI tracks a "current project" in its config
and scopes app commands to it unless --project is passed.

Quickstart:
  mortise login
  mortise project list
  mortise project use my-project
  mortise app create --source image --image nginx:1.27 --name web
  mortise deploy web --env production --image nginx:1.27
`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newLoginCmd())
	cmd.AddCommand(newProjectCmd())
	cmd.AddCommand(newAppCmd())
	cmd.AddCommand(newDeployCmd())
	cmd.AddCommand(newRollbackCmd())
	cmd.AddCommand(newPromoteCmd())
	cmd.AddCommand(newLogsCmd())
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newDomainCmd())
	cmd.AddCommand(newTokenCmd())
	cmd.AddCommand(newEnvCmd())
	cmd.AddCommand(newSecretCmd())
	cmd.AddCommand(newGitProviderCmd())
	cmd.AddCommand(newPlatformCmd())
	cmd.AddCommand(newRepoCmd())
	return cmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
