package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	var project, env, image string
	cmd := &cobra.Command{
		Use:   "deploy <app>",
		Short: "Trigger a deployment for an app in a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if image == "" {
				return fmt.Errorf("--image is required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.Deploy(p, args[0], env, image); err != nil {
				return err
			}
			fmt.Printf("Deploy triggered for %q in project %q.\n", args[0], p)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Target environment")
	cmd.Flags().StringVar(&image, "image", "", "Image reference to deploy")
	return cmd
}
