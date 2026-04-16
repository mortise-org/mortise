package main

import (
	"os"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "logs <app>",
		Short: "Stream logs from an app in a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			return c.StreamLogs(c.ResolveProject(project), args[0], env, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment (default: production)")
	return cmd
}
