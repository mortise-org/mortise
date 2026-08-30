package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mortise-org/mortise/internal/version"
)

func newVersionCmd() *cobra.Command {
	var clientOnly bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI and operator build versions",
		Long: `Print the CLI build version and, unless --client is given, the version of
the operator the CLI is logged in to. The operator's version comes from the
running binary, so it names what production is actually running, not what
was last merged or tagged.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Client:   %s\n", version.String())
			if clientOnly {
				return nil
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			return printOperatorVersion(out, c)
		},
	}
	cmd.Flags().BoolVar(&clientOnly, "client", false, "Print only the CLI version; do not contact the operator")
	return cmd
}

func printOperatorVersion(out io.Writer, c *Client) error {
	p, err := c.GetPlatform()
	if err != nil {
		return err
	}
	op := p.Operator
	if op.Version == "" {
		// An operator built before this field existed answers with nothing;
		// say so rather than printing an empty line that looks like a bug.
		fmt.Fprintln(out, "Operator: unknown (operator predates version reporting)")
		return nil
	}
	fmt.Fprintf(out, "Operator: %s (%s)\n", op.Version, op.Commit)
	return nil
}
