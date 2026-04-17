package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

type domainsResponse struct {
	Primary string   `json:"primary"`
	Custom  []string `json:"custom"`
}

func newDomainCmd() *cobra.Command {
	var projectFlag string

	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage custom domains for an app",
	}

	listCmd := &cobra.Command{
		Use:   "list <app>",
		Short: "List domains for an app environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			project := c.ResolveProject(projectFlag)
			if project == "" {
				return fmt.Errorf("no project set; pass --project or run 'mortise project use'")
			}

			env, _ := cmd.Flags().GetString("env")
			if env == "" {
				env = "production"
			}

			var resp domainsResponse
			u := fmt.Sprintf("%s/domains?environment=%s",
				c.appBase(project, args[0]), url.QueryEscape(env))
			if err := c.doJSON(http.MethodGet, u, nil, &resp); err != nil {
				return err
			}

			fmt.Printf("Primary: %s\n", resp.Primary)
			if len(resp.Custom) > 0 {
				fmt.Println("Custom domains:")
				for _, d := range resp.Custom {
					fmt.Printf("  %s\n", d)
				}
			} else {
				fmt.Println("No custom domains configured.")
			}
			return nil
		},
	}
	listCmd.Flags().String("env", "production", "Environment name")
	listCmd.Flags().StringVar(&projectFlag, "project", "", "Project (overrides current)")

	addCmd := &cobra.Command{
		Use:   "add <app> <domain>",
		Short: "Add a custom domain to an app environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			project := c.ResolveProject(projectFlag)
			if project == "" {
				return fmt.Errorf("no project set; pass --project or run 'mortise project use'")
			}

			env, _ := cmd.Flags().GetString("env")
			if env == "" {
				env = "production"
			}

			var resp domainsResponse
			u := fmt.Sprintf("%s/domains?environment=%s",
				c.appBase(project, args[0]), url.QueryEscape(env))
			body := map[string]string{"domain": args[1]}
			if err := c.doJSON(http.MethodPost, u, body, &resp); err != nil {
				return err
			}

			fmt.Printf("Added %s\n", args[1])
			if resp.Primary != "" {
				fmt.Printf("CNAME %s -> %s\n", args[1], resp.Primary)
			}
			return nil
		},
	}
	addCmd.Flags().String("env", "production", "Environment name")
	addCmd.Flags().StringVar(&projectFlag, "project", "", "Project (overrides current)")

	removeCmd := &cobra.Command{
		Use:   "remove <app> <domain>",
		Short: "Remove a custom domain from an app environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			project := c.ResolveProject(projectFlag)
			if project == "" {
				return fmt.Errorf("no project set; pass --project or run 'mortise project use'")
			}

			env, _ := cmd.Flags().GetString("env")
			if env == "" {
				env = "production"
			}

			u := fmt.Sprintf("%s/domains/%s?environment=%s",
				c.appBase(project, args[0]),
				url.PathEscape(args[1]),
				url.QueryEscape(env))
			if err := c.doJSON(http.MethodDelete, u, nil, nil); err != nil {
				return err
			}

			fmt.Printf("Removed %s\n", args[1])
			return nil
		},
	}
	removeCmd.Flags().String("env", "production", "Environment name")
	removeCmd.Flags().StringVar(&projectFlag, "project", "", "Project (overrides current)")

	cmd.AddCommand(listCmd, addCmd, removeCmd)
	return cmd
}
