package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRollbackCmd() *cobra.Command {
	var project, env string
	var index int
	cmd := &cobra.Command{
		Use:   "rollback <app>",
		Short: "Roll back an app to a previous deployment",
		Long: `Roll back an app's environment to a previous deployment from its deploy history.

Use --index to specify which deploy history entry to roll back to (0 = most
recent previous deploy). Without --index, defaults to 0.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if env == "" {
				return fmt.Errorf("--env is required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			record, err := c.Rollback(p, args[0], env, index)
			if err != nil {
				return err
			}
			fmt.Printf("Rolled back %q (env %q) to %s", args[0], env, record.Image)
			if record.GitSHA != "" {
				fmt.Printf(" (commit %s)", record.GitSHA[:min(7, len(record.GitSHA))])
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Target environment (required)")
	cmd.Flags().IntVar(&index, "index", 0, "Deploy history index to roll back to (0 = most recent previous)")
	return cmd
}
