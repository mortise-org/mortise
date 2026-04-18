package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Browse repositories from connected git providers",
	}
	cmd.AddCommand(newRepoListCmd())
	cmd.AddCommand(newRepoBranchesCmd())
	return cmd
}

func newRepoListCmd() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories from a git provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" {
				return fmt.Errorf("--provider is required")
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			repos, err := c.ListRepos(provider)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tLANGUAGE\tDEFAULT BRANCH\tPRIVATE")
			for _, r := range repos {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", r.FullName, r.Language, r.DefaultBranch, r.Private)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Git provider name")
	_ = cmd.MarkFlagRequired("provider")
	return cmd
}

func newRepoBranchesCmd() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:   "branches <owner/repo>",
		Short: "List branches for a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" {
				return fmt.Errorf("--provider is required")
			}
			parts := strings.SplitN(args[0], "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("expected owner/repo format, got %q", args[0])
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			branches, err := c.ListBranches(parts[0], parts[1], provider)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tDEFAULT")
			for _, b := range branches {
				fmt.Fprintf(tw, "%s\t%v\n", b.Name, b.Default)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Git provider name")
	_ = cmd.MarkFlagRequired("provider")
	return cmd
}
