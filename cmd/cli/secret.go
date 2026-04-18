package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets for apps (write-only — values are never returned)",
	}
	cmd.AddCommand(newSecretListCmd())
	cmd.AddCommand(newSecretSetCmd())
	cmd.AddCommand(newSecretDeleteCmd())
	return cmd
}

func newSecretListCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "list <app>",
		Short: "List secrets for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			secrets, err := c.ListSecrets(p, args[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKEYS")
			for _, s := range secrets {
				fmt.Fprintf(tw, "%s\t%v\n", s.Name, s.Keys)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}

func newSecretSetCmd() *cobra.Command {
	var project, env, value string
	cmd := &cobra.Command{
		Use:   "set <app> <name>",
		Short: "Set a secret (prompts for value securely)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			secretValue := value
			if secretValue == "" {
				fmt.Print("Secret value: ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if err != nil {
					return fmt.Errorf("reading secret value: %w", err)
				}
				secretValue = string(pw)
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.SetSecret(p, args[0], args[1], secretValue); err != nil {
				return err
			}
			fmt.Printf("Secret %q set.\n", args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	cmd.Flags().StringVar(&value, "value", "", "Secret value (for scripting; omit to prompt securely)")
	return cmd
}

func newSecretDeleteCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "delete <app> <name>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			if err := c.DeleteSecret(p, args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("Secret %q deleted.\n", args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment name")
	return cmd
}
