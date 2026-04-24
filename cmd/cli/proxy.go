package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newProxyCmd() *cobra.Command {
	var project, env string
	cmd := &cobra.Command{
		Use:   "proxy <app>",
		Short: "Open a local proxy to an app",
		Long: `Start a local reverse proxy to an app running in Mortise.
The proxy routes through the Mortise API server (no kubectl needed).
Press Ctrl-C to stop.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			app := args[0]

			resp, err := c.Connect(p, app, env)
			if err != nil {
				return fmt.Errorf("connecting to %s: %w", app, err)
			}

			fmt.Fprintf(os.Stderr, "Proxying %s → %s\n", app, resp.URL)
			fmt.Fprintf(os.Stderr, "Press Ctrl-C to stop.\n")
			fmt.Println(resp.URL)

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig

			fmt.Fprintln(os.Stderr, "\nDisconnecting...")
			if err := c.Disconnect(p, app); err != nil {
				fmt.Fprintf(os.Stderr, "warning: disconnect failed: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&env, "env", "", "Environment (default: production)")
	return cmd
}
