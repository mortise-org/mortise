package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newGitProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git-provider",
		Short: "Manage git providers",
	}
	cmd.AddCommand(newGitProviderListCmd())
	cmd.AddCommand(newGitProviderCreateCmd())
	cmd.AddCommand(newGitProviderDeleteCmd())
	cmd.AddCommand(newGitProviderConnectGitHubCmd())
	return cmd
}

func newGitProviderListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured git providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			providers, err := c.ListGitProviders()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tHOST\tPHASE\tMODE")
			for _, p := range providers {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Type, p.Host, p.Phase, p.Mode)
			}
			return tw.Flush()
		},
	}
}

func newGitProviderCreateCmd() *cobra.Command {
	var name, providerType, host, clientID, clientSecret, webhookSecret string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a git provider",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			req := CreateGitProviderRequest{
				Name:          name,
				Type:          providerType,
				Host:          host,
				ClientID:      clientID,
				ClientSecret:  clientSecret,
				WebhookSecret: webhookSecret,
			}
			if err := c.CreateGitProvider(req); err != nil {
				return err
			}
			fmt.Printf("Git provider %q created.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Provider name")
	cmd.Flags().StringVar(&providerType, "type", "", "Provider type (github|gitlab|gitea)")
	cmd.Flags().StringVar(&host, "host", "", "Host URL")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client ID")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client secret")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "Webhook secret")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newGitProviderDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a git provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			if err := c.DeleteGitProvider(args[0]); err != nil {
				return err
			}
			fmt.Printf("Git provider %q deleted.\n", args[0])
			return nil
		},
	}
}

func newGitProviderConnectGitHubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect [provider]",
		Short: "Connect a git provider via device flow",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := "github"
			if len(args) > 0 {
				provider = args[0]
			}

			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			code, err := c.RequestDeviceCode(provider)
			if err != nil {
				return fmt.Errorf("requesting device code: %w", err)
			}

			fmt.Printf("Go to %s and enter code: %s\n", code.VerificationURI, code.UserCode)

			// Try to open the URL in the browser.
			openBrowser(code.VerificationURI)

			interval := code.Interval
			if interval < 5 {
				interval = 5
			}

			for {
				time.Sleep(time.Duration(interval) * time.Second)
				poll, err := c.PollDeviceCode(provider, code.DeviceCode)
				if err != nil {
					return fmt.Errorf("polling device code: %w", err)
				}
				switch poll.Status {
				case "complete":
					fmt.Printf("%s connected!\n", provider)
					return nil
				case "expired":
					return fmt.Errorf("device code expired — please try again")
				}
				// "authorization_pending" or "slow_down" — keep polling
			}
		},
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}
