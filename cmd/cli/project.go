package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects",
		Long: `Manage projects — the top-level grouping for apps.

Every app lives inside exactly one project. The CLI tracks a "current project"
in its config file; most app-scoped commands use it unless overridden with
--project.`,
	}
	cmd.AddCommand(newProjectListCmd())
	cmd.AddCommand(newProjectCreateCmd())
	cmd.AddCommand(newProjectDeleteCmd())
	cmd.AddCommand(newProjectUseCmd())
	cmd.AddCommand(newProjectShowCmd())
	return cmd
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			projects, err := c.ListProjects()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tPHASE\tAPPS\tDESCRIPTION")
			for _, p := range projects {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.Name, p.Phase, p.AppCount, p.Description)
			}
			return w.Flush()
		},
	}
}

func newProjectCreateCmd() *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p, err := c.CreateProject(args[0], description)
			if err != nil {
				return err
			}
			fmt.Printf("Project %q created (namespace: %s).\n", p.Name, p.Namespace)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Optional project description")
	return cmd
}

func newProjectDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a project and all apps inside it (admin only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if !yes {
				fmt.Printf("This will delete project %q and every app inside it. Continue? [y/N]: ", name)
				reader := bufio.NewReader(os.Stdin)
				line, _ := reader.ReadString('\n')
				if !strings.EqualFold(strings.TrimSpace(line), "y") {
					fmt.Println("Aborted.")
					return nil
				}
			}

			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			if err := c.DeleteProject(name); err != nil {
				return err
			}
			fmt.Printf("Project %q deletion accepted.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func newProjectUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the current project context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			cfg.CurrentProject = args[0]
			if err := saveConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("Current project set to %q.\n", args[0])
			return nil
		},
	}
}

func newProjectShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			name := c.ResolveProject("")
			p, err := c.GetProject(name)
			if err != nil {
				return err
			}
			fmt.Printf("Name:        %s\n", p.Name)
			fmt.Printf("Namespace:   %s\n", p.Namespace)
			fmt.Printf("Phase:       %s\n", p.Phase)
			fmt.Printf("App count:   %d\n", p.AppCount)
			if p.Description != "" {
				fmt.Printf("Description: %s\n", p.Description)
			}
			if p.CreatedAt != "" {
				fmt.Printf("Created:     %s\n", p.CreatedAt)
			}
			return nil
		},
	}
}
