package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	mortisev1alpha1 "github.com/mortise-org/mortise/api/v1alpha1"
)

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage apps in a project",
	}
	cmd.AddCommand(newAppListCmd())
	cmd.AddCommand(newAppCreateCmd())
	cmd.AddCommand(newAppUpdateCmd())
	cmd.AddCommand(newAppDeleteCmd())
	return cmd
}

func newAppListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List apps in a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			apps, err := c.ListApps(c.ResolveProject(project))
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSOURCE\tPHASE")
			for _, a := range apps {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, a.Spec.Source.Type, a.Status.Phase)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project to list apps in (default: current project)")
	return cmd
}

func newAppCreateCmd() *cobra.Command {
	var project, source, image, name string
	var storage []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an app in a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if source == "" {
				source = "image"
			}
			volumes := make([]mortisev1alpha1.VolumeSpec, 0, len(storage))
			for _, s := range storage {
				v, err := parseStorageFlag(s)
				if err != nil {
					return err
				}
				volumes = append(volumes, v)
			}
			req := CreateAppRequest{
				Name: name,
				Spec: mortisev1alpha1.AppSpec{
					Source: mortisev1alpha1.AppSource{
						Type:  mortisev1alpha1.SourceType(source),
						Image: image,
					},
					Storage: volumes,
				},
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			app, err := c.CreateApp(c.ResolveProject(project), req)
			if err != nil {
				return err
			}
			fmt.Printf("App %q created in project %q.\n", app.Name, c.ResolveProject(project))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project to create the app in (default: current project)")
	cmd.Flags().StringVar(&source, "source", "image", "Source type (git|image)")
	cmd.Flags().StringVar(&image, "image", "", "Container image reference (for source=image)")
	cmd.Flags().StringVar(&name, "name", "", "App name")
	cmd.Flags().StringArrayVar(&storage, "storage", nil, "Persistent volume as name:mountPath[:size[:storageClass]] (repeatable)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func parseStorageFlag(s string) (mortisev1alpha1.VolumeSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 4 {
		return mortisev1alpha1.VolumeSpec{}, fmt.Errorf("invalid --storage %q: want name:mountPath[:size[:storageClass]]", s)
	}
	v := mortisev1alpha1.VolumeSpec{Name: parts[0], MountPath: parts[1]}
	if v.Name == "" {
		return v, fmt.Errorf("invalid --storage %q: name is empty", s)
	}
	if !strings.HasPrefix(v.MountPath, "/") {
		return v, fmt.Errorf("invalid --storage %q: mountPath must be an absolute path", s)
	}
	if len(parts) > 2 && parts[2] != "" {
		q, err := resource.ParseQuantity(parts[2])
		if err != nil {
			return v, fmt.Errorf("invalid --storage %q: bad size %q: %w", s, parts[2], err)
		}
		v.Size = q
	}
	if len(parts) == 4 {
		v.StorageClass = parts[3]
	}
	return v, nil
}

func newAppUpdateCmd() *cobra.Command {
	var project, image, env string
	var replicas int32
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an app (read-modify-write)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			p := c.ResolveProject(project)
			app, err := c.GetApp(p, args[0])
			if err != nil {
				return fmt.Errorf("fetching app: %w", err)
			}
			if cmd.Flags().Changed("image") {
				app.Spec.Source.Image = image
			}
			if cmd.Flags().Changed("replicas") {
				found := false
				for i := range app.Spec.Environments {
					if app.Spec.Environments[i].Name == env {
						app.Spec.Environments[i].Replicas = &replicas
						found = true
						break
					}
				}
				if !found {
					app.Spec.Environments = append(app.Spec.Environments, mortisev1alpha1.Environment{
						Name:     env,
						Replicas: &replicas,
					})
				}
			}
			if _, err := c.UpdateApp(p, args[0], app.Spec); err != nil {
				return err
			}
			fmt.Printf("App %q updated.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project (default: current project)")
	cmd.Flags().StringVar(&image, "image", "", "Container image reference")
	cmd.Flags().Int32Var(&replicas, "replicas", 0, "Replica count")
	cmd.Flags().StringVar(&env, "env", "production", "Environment name (used with --replicas)")
	return cmd
}

func newAppDeleteCmd() *cobra.Command {
	var project string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an app from a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !yes {
				fmt.Printf("This will delete app %q and all its resources (deployments, services, ingresses, volumes). Continue? [y/N]: ", name)
				reader := bufio.NewReader(os.Stdin)
				line, readErr := reader.ReadString('\n')
				if readErr != nil {
					return fmt.Errorf("failed to read confirmation from stdin (if running non-interactively, use --yes to skip confirmation): %w", readErr)
				}
				if !strings.EqualFold(strings.TrimSpace(line), "y") {
					fmt.Println("Aborted.")
					return nil
				}
			}
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			if err := c.DeleteApp(c.ResolveProject(project), name); err != nil {
				return err
			}
			fmt.Printf("App %q deleted.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project the app belongs to (default: current project)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}
