package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPlatformCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "View and configure platform settings",
	}
	cmd.AddCommand(newPlatformGetCmd())
	cmd.AddCommand(newPlatformSetCmd())
	return cmd
}

func newPlatformGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show current platform configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p, err := c.GetPlatform()
			if err != nil {
				return err
			}
			fmt.Printf("Domain:   %s\n", p.Domain)
			fmt.Printf("Registry: %s\n", p.Registry.URL)
			fmt.Printf("BuildKit: %s\n", p.Build.BuildkitAddr)
			return nil
		},
	}
}

func newPlatformSetCmd() *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update platform configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p, err := c.PatchPlatform(PlatformPatchRequest{Domain: domain})
			if err != nil {
				return err
			}
			fmt.Printf("Platform updated. Domain: %s\n", p.Domain)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "Platform domain")
	return cmd
}
