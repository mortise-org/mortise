package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage deploy tokens for apps",
	}
	cmd.AddCommand(newTokenCreateCmd())
	cmd.AddCommand(newTokenListCmd())
	cmd.AddCommand(newTokenRevokeCmd())
	return cmd
}

func newTokenCreateCmd() *cobra.Command {
	var project, name string
	cmd := &cobra.Command{
		Use:   "create <app> <env>",
		Short: "Create a deploy token for an app environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			resp, err := c.CreateToken(p, args[0], args[1], name)
			if err != nil {
				return err
			}
			fmt.Println(resp.Token)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&name, "name", "", "Token name (e.g. github-ci)")
	return cmd
}

func newTokenListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list <app>",
		Short: "List deploy tokens for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			tokens, err := c.ListTokens(p, args[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tENVIRONMENT\tCREATED")
			for _, t := range tokens {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.Name, t.Environment, t.CreatedAt)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	return cmd
}

func newTokenRevokeCmd() *cobra.Command {
	var project, app string
	cmd := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a deploy token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app == "" {
				return fmt.Errorf("--app is required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.RevokeToken(p, app, args[0]); err != nil {
				return err
			}
			fmt.Printf("Token %q revoked.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&app, "app", "", "App the token belongs to")
	return cmd
}
