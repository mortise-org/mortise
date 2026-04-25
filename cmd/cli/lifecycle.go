package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Start Mortise (ensure cluster running + port-forward)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check Docker
			if err := runQuiet("docker", "info"); err != nil {
				return fmt.Errorf("docker is not running; start Docker Desktop and try again")
			}

			// Check if k3d cluster exists
			out, err := output("k3d", "cluster", "list", "-o", "json")
			if err != nil {
				return fmt.Errorf("k3d not found. Run 'mortise install' first")
			}

			if !strings.Contains(out, `"mortise"`) {
				return fmt.Errorf("mortise cluster not found; run 'mortise install' first")
			}

			if !strings.Contains(out, `"running"`) {
				return fmt.Errorf("mortise cluster exists but is not running; start Docker and try again")
			}

			fmt.Println("Cluster 'mortise' is running")

			// Kill existing port-forwards
			_ = runQuiet("pkill", "-f", "port-forward.*svc/mortise")

			// Start port-forward
			pf := exec.Command("kubectl", "port-forward", "-n", "mortise-system", "svc/mortise", "8090:80")
			pf.Stdout = nil
			pf.Stderr = nil
			if err := pf.Start(); err != nil {
				return fmt.Errorf("failed to start port-forward: %w", err)
			}

			fmt.Println("Mortise is up at http://localhost:8090")
			fmt.Printf("Port-forward PID: %d\n", pf.Process.Pid)
			return nil
		},
	}
}

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop Mortise port-forward (cluster stays running)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = runQuiet("pkill", "-f", "port-forward.*svc/mortise")
			fmt.Println("Mortise port-forward stopped. Cluster is still running.")
			fmt.Println("To destroy the cluster entirely: mortise destroy")
			return nil
		},
	}
}

func newDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy",
		Short: "Delete the Mortise cluster entirely",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = runQuiet("pkill", "-f", "port-forward.*svc/mortise")

			fmt.Println("Deleting Mortise cluster...")
			if err := run("k3d", "cluster", "delete", "mortise"); err != nil {
				// Fallback: clean up Mortise resources directly
				_ = run("kubectl", "delete", "namespace", "mortise-system")
				_ = run("kubectl", "delete", "namespace", "mortise-deps")
				_ = run("kubectl", "delete", "crd", "-l", "app.kubernetes.io/managed-by=mortise")
				_ = run("kubectl", "delete", "clusterrole", "-l", "app.kubernetes.io/managed-by=mortise")
				_ = run("kubectl", "delete", "clusterrolebinding", "-l", "app.kubernetes.io/managed-by=mortise")
				fmt.Println("Cleaned up Mortise resources")
				return nil
			}
			fmt.Println("Mortise cluster destroyed")
			return nil
		},
	}
}

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cluster-status",
		Short: "Show Mortise cluster health",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check Docker
			dockerOK := runQuiet("docker", "info") == nil

			// Check cluster
			clusterOut, _ := output("k3d", "cluster", "list", "--no-headers")
			clusterRunning := strings.Contains(clusterOut, "mortise")

			// Check operator pod
			podOut, _ := output("kubectl", "get", "pods", "-n", "mortise-system", "--no-headers", "-l", "app.kubernetes.io/name=mortise")
			operatorRunning := strings.Contains(podOut, "Running")

			// Check port-forward
			pfOut, _ := output("pgrep", "-f", "port-forward.*svc/mortise")
			pfRunning := strings.TrimSpace(pfOut) != ""

			fmt.Println("Mortise Status")
			fmt.Println("──────────────")
			printStatus("Docker", dockerOK)
			printStatus("Cluster", clusterRunning)
			printStatus("Operator", operatorRunning)
			printStatus("Port-forward", pfRunning)

			if pfRunning {
				fmt.Println("\nUI: http://localhost:8090")
			} else if operatorRunning {
				fmt.Println("\nRun 'mortise up' to start the port-forward")
			} else if !clusterRunning {
				fmt.Println("\nRun 'mortise install' to set up Mortise")
			}

			return nil
		},
	}
}

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install Mortise (k3d/k3s, cert-manager, BuildKit, registry, operator)",
		Long:  "Runs the quick-mortise installer. Equivalent to 'bash scripts/install.sh'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Find install.sh relative to the binary or in known system locations
			var locations []string

			// Check relative to the binary
			if exe, err := os.Executable(); err == nil {
				dir := strings.TrimSuffix(exe, "/mortise")
				dir = strings.TrimSuffix(dir, "/bin/mortise")
				locations = append(locations, dir+"/scripts/install.sh")
			}

			locations = append(locations, "/usr/local/share/mortise/install.sh")

			for _, loc := range locations {
				if _, err := os.Stat(loc); err == nil {
					return run("bash", loc)
				}
			}

			return fmt.Errorf("install.sh not found. Run from the mortise repo or download from https://github.com/mortise-org/mortise")
		},
	}
}

// helpers

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

func printStatus(label string, ok bool) {
	if ok {
		fmt.Printf("  %-14s \033[32mrunning\033[0m\n", label)
	} else {
		fmt.Printf("  %-14s \033[31mstopped\033[0m\n", label)
	}
}
