package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage environment variables for apps",
	}
	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvSetCmd())
	cmd.AddCommand(newEnvUnsetCmd())
	cmd.AddCommand(newEnvImportCmd())
	cmd.AddCommand(newEnvPullCmd())
	return cmd
}

func newEnvListCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "list <app>",
		Short: "List environment variables for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			vars, err := c.GetEnv(p, args[0], env)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVALUE")
			for _, v := range vars {
				fmt.Fprintf(tw, "%s\t%s\n", v.Name, v.Value)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}

func newEnvSetCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "set <app> KEY=value [KEY2=value2 ...]",
		Short: "Set environment variables",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			set := make(map[string]string)
			for _, kv := range args[1:] {
				idx := strings.IndexByte(kv, '=')
				if idx < 1 {
					return fmt.Errorf("invalid KEY=value pair: %q", kv)
				}
				set[kv[:idx]] = kv[idx+1:]
			}
			if err := c.PatchEnv(p, args[0], env, set, nil); err != nil {
				return err
			}
			fmt.Printf("Updated %d variable(s).\n", len(set))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}

func newEnvUnsetCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "unset <app> KEY [KEY2 ...]",
		Short: "Unset environment variables",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.PatchEnv(p, args[0], env, nil, args[1:]); err != nil {
				return err
			}
			fmt.Printf("Removed %d variable(s).\n", len(args)-1)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}

func newEnvImportCmd() *cobra.Command {
	var project, env, file string
	cmd := &cobra.Command{
		Use:   "import <app>",
		Short: "Import env vars from a .env file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)

			var data []byte
			if file == "" || file == "-" {
				data, err = os.ReadFile("/dev/stdin")
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("reading .env file: %w", err)
			}

			if err := c.ImportEnv(p, args[0], env, string(data)); err != nil {
				return err
			}
			fmt.Println("Imported.")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	cmd.Flags().StringVar(&file, "file", "", "Path to .env file (or stdin)")
	return cmd
}

func newEnvPullCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "pull <app>",
		Short: "Output env vars as .env format to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			vars, err := c.GetEnv(p, args[0], env)
			if err != nil {
				return err
			}
			for _, v := range vars {
				fmt.Printf("%s=%s\n", v.Name, v.Value)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}
