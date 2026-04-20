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
	cmd.AddCommand(newProjectEnvCmd())
	return cmd
}

func newProjectEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage a project's environments",
		Long: `Manage a project's environments (production, staging, …).

Environments live on the project: every app inside a project auto-exists in
every env. Use app-level env overrides for per-env customization.`,
	}
	cmd.AddCommand(newProjectEnvListCmd())
	cmd.AddCommand(newProjectEnvCreateCmd())
	cmd.AddCommand(newProjectEnvDeleteCmd())
	cmd.AddCommand(newProjectEnvRenameCmd())
	return cmd
}

func newProjectEnvListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a project's environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			envs, err := c.ListProjectEnvs(c.ResolveProject(project))
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tORDER\tHEALTH")
			for _, env := range envs {
				_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", env.Name, env.DisplayOrder, env.Health)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project name (defaults to current)")
	return cmd
}

func newProjectEnvCreateCmd() *cobra.Command {
	var project string
	var order int
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Add a new environment to a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			env, err := c.CreateProjectEnv(c.ResolveProject(project), args[0], order)
			if err != nil {
				return err
			}
			fmt.Printf("Environment %q added to project.\n", env.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project name (defaults to current)")
	cmd.Flags().IntVar(&order, "order", 0, "Display order for UI navbar")
	return cmd
}

func newProjectEnvDeleteCmd() *cobra.Command {
	var project string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove an environment from a project",
		Long: `Remove an environment from a project.

The server rejects the call if any app still carries an override for the env;
remove the override from each offending app first (mortise env unset / edit
spec.environments) and retry.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				fmt.Printf("This will remove environment %q and garbage-collect every app's resources in it. Continue? [y/N]: ", name)
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
			if err := c.DeleteProjectEnv(c.ResolveProject(project), name); err != nil {
				return err
			}
			fmt.Printf("Environment %q deleted.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project name (defaults to current)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func newProjectEnvRenameCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename an environment (cascades to app overrides)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			env, err := c.RenameProjectEnv(c.ResolveProject(project), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("Environment renamed %q → %q.\n", args[0], env.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project name (defaults to current)")
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
