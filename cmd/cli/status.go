package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type AppDetailResponse struct {
	Name   string          `json:"name"`
	Source AppSourceResp   `json:"source"`
	Status AppDetailStatus `json:"status"`
}

type AppDetailStatus struct {
	Phase        string          `json:"phase,omitempty"`
	Environments []EnvStatusResp `json:"environments,omitempty"`
}

type EnvStatusResp struct {
	Name          string `json:"name"`
	ReadyReplicas int32  `json:"readyReplicas"`
	CurrentImage  string `json:"currentImage,omitempty"`
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <app>",
		Short: "Show app status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			var resp AppDetailResponse
			if err := c.doJSON("GET", "/api/apps/"+args[0], nil, &resp); err != nil {
				return err
			}
			fmt.Printf("App:    %s\n", resp.Name)
			fmt.Printf("Source: %s\n", resp.Source.Type)
			fmt.Printf("Phase:  %s\n", resp.Status.Phase)
			if len(resp.Status.Environments) > 0 {
				fmt.Println()
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "ENV\tREADY\tIMAGE")
				for _, e := range resp.Status.Environments {
					fmt.Fprintf(w, "%s\t%d\t%s\n", e.Name, e.ReadyReplicas, e.CurrentImage)
				}
				w.Flush()
			}
			return nil
		},
	}
}
