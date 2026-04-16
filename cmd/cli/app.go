package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	mortisev1alpha1 "github.com/MC-Meesh/mortise/api/v1alpha1"
)

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage apps in a project",
	}
	cmd.AddCommand(newAppListCmd())
	cmd.AddCommand(newAppCreateCmd())
	cmd.AddCommand(newAppDeleteCmd())
	return cmd
}

func newAppListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List apps in a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			apps, err := c.ListApps(c.ResolveProject(project))
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSOURCE\tPHASE")
			for _, a := range apps {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, a.Spec.Source.Type, a.Status.Phase)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project to list apps in (default: current project)")
	return cmd
}

func newAppCreateCmd() *cobra.Command {
	var project, source, image, name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an app in a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if source == "" {
				source = "image"
			}
			req := CreateAppRequest{
				Name: name,
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceType(source),
						Image: image,
					},
				},
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			app, err := c.CreateApp(c.ResolveProject(project), req)
			if err != nil {
				return err
			}
			fmt.Printf("App %q created in project %q.\n", app.Name, c.ResolveProject(project))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project to create the app in (default: current project)")
	cmd.Flags().StringVar(&source, "source", "image", "Source type (git|image)")
	cmd.Flags().StringVar(&image, "image", "", "Container image reference (for source=image)")
	cmd.Flags().StringVar(&name, "name", "", "App name")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newAppDeleteCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an app from a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			if err := c.DeleteApp(c.ResolveProject(project), args[0]); err != nil {
				return err
			}
			fmt.Printf("App %q deleted.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	return cmd
}
