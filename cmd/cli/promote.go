package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var project, from, to string
	cmd := &cobra.Command{
		Use:   "promote <app>",
		Short: "Promote an app's image from one environment to another",
		Long: `Promote copies the current image digest from the source environment to
the target environment without rebuilding. This is the standard way to
move a verified build from staging to production.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("--from and --to are required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.Promote(p, args[0], from, to); err != nil {
				return err
			}
			fmt.Printf("Promoted %q from %s to %s.\n", args[0], from, to)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	cmd.Flags().StringVar(&from, "from", "", "Source environment (required)")
	cmd.Flags().StringVar(&to, "to", "", "Target environment (required)")
	return cmd
}
