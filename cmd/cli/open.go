package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newOpenCmd() *cobra.Command {
	var project, env string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "open <app>",
		Short: "Open an app in the browser",
		Long: `Connect to an app running in Mortise and open it in the default browser.
The server-side proxy stays alive after this command exits.
Use "mortise proxy" if you need to block and disconnect on Ctrl-C.`,
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

			fmt.Println(resp.URL)

			if !noBrowser {
				openBrowser(resp.URL)
				fmt.Fprintf(os.Stderr, "Opened %s in browser\n", app)
			}

			return nil
		},
	}
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project (default: current project)")
	cmd.Flags().StringVarP(&env, "env", "e", "", "Environment (default: production)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print URL only, don't open browser")
	return cmd
}
