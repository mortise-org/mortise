package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

type DeployRequest struct {
	Env   string `json:"env,omitempty"`
	Image string `json:"image,omitempty"`
}

func newDeployCmd() *cobra.Command {
	var env, image string
	cmd := &cobra.Command{
		Use:   "deploy <app>",
		Short: "Trigger a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			req := DeployRequest{Env: env, Image: image}
			if err := c.doJSON("POST", "/api/deploy", map[string]any{
				"app":   args[0],
				"env":   req.Env,
				"image": req.Image,
			}, nil); err != nil {
				return err
			}
			fmt.Printf("Deploy triggered for %q.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "Target environment")
	cmd.Flags().StringVar(&image, "image", "", "Image reference to deploy")
	return cmd
}
