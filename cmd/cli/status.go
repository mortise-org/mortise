package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "status <app>",
		Short: "Show status for an app in a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			app, err := c.GetApp(c.ResolveProject(project), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("App:     %s\n", app.Name)
			fmt.Printf("Project: %s\n", c.ResolveProject(project))
			fmt.Printf("Source:  %s\n", app.Spec.Source.Type)
			fmt.Printf("Phase:   %s\n", app.Status.Phase)
			if len(app.Status.Environments) > 0 {
				fmt.Println()
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "ENV\tREADY\tIMAGE")
				for _, e := range app.Status.Environments {
					_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", e.Name, e.ReadyReplicas, e.CurrentImage)
				}
				_ = w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	return cmd
}
