package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// API response types (mirrors relevant CRD fields for JSON transport)

type AppResponse struct {
	Name   string        `json:"name"`
	Source AppSourceResp `json:"source"`
	Status AppStatusResp `json:"status"`
}

type AppSourceResp struct {
	Type  string `json:"type"`
	Image string `json:"image,omitempty"`
	Repo  string `json:"repo,omitempty"`
}

type AppStatusResp struct {
	Phase string `json:"phase,omitempty"`
}

type AppListResponse struct {
	Items []AppResponse `json:"items"`
}

type CreateAppRequest struct {
	Name   string             `json:"name"`
	Source CreateAppSourceReq `json:"source"`
}

type CreateAppSourceReq struct {
	Type  string `json:"type"`
	Image string `json:"image,omitempty"`
}

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage apps",
	}
	cmd.AddCommand(newAppListCmd())
	cmd.AddCommand(newAppCreateCmd())
	cmd.AddCommand(newAppDeleteCmd())
	return cmd
}

func newAppListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			var resp AppListResponse
			if err := c.doJSON("GET", "/api/apps", nil, &resp); err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSOURCE\tPHASE")
			for _, a := range resp.Items {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, a.Source.Type, a.Status.Phase)
			}
			return w.Flush()
		},
	}
}

func newAppCreateCmd() *cobra.Command {
	var source, image, name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an app",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if source == "" {
				source = "image"
			}
			req := CreateAppRequest{
				Name: name,
				Source: CreateAppSourceReq{
					Type:  source,
					Image: image,
				},
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			var resp AppResponse
			if err := c.doJSON("POST", "/api/apps", req, &resp); err != nil {
				return err
			}
			fmt.Printf("App %q created.\n", resp.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "image", "Source type (git|image)")
	cmd.Flags().StringVar(&image, "image", "", "Container image reference")
	cmd.Flags().StringVar(&name, "name", "", "App name")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newAppDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			if err := c.doJSON("DELETE", "/api/apps/"+args[0], nil, nil); err != nil {
				return err
			}
			fmt.Printf("App %q deleted.\n", args[0])
			return nil
		},
	}
}
