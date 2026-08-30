package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mortise-org/mortise/internal/auth"
)

// The admin commands talk to Kubernetes directly, like `mortise diff`, and
// not to the API: they exist for the case where nobody can log in. A lost
// admin password, or an API whose every saved token has expired, has no
// API-side recovery by construction. Cluster access to mortise-system is
// the credential here, which is the same bar as reading the user Secrets.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Cluster-side user administration (no login required)",
		Long: `Manage Mortise users directly in the cluster, without an API login.

These commands need a kubeconfig that can read and write Secrets in
mortise-system. They are the recovery path when no one can log in; for
day-to-day user management use the UI or the /api/admin/users endpoints.`,
	}
	cmd.AddCommand(newAdminResetPasswordCmd())
	cmd.AddCommand(newAdminCreateUserCmd())
	return cmd
}

func newAdminResetPasswordCmd() *cobra.Command {
	var kubeconfig, kubecontext string
	var stdin bool
	cmd := &cobra.Command{
		Use:   "reset-password <email>",
		Short: "Set a user's password and invalidate their existing tokens",
		Long: `Set a new password for an existing user. Every token issued before the
reset stops validating: tokens carry the user's password generation and it
is bumped here. Read the password from the terminal, or from stdin with
--password-stdin for scripts (the first line is used).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := readPassword(cmd.InOrStdin(), cmd.ErrOrStderr(), stdin, "New password: ")
			if err != nil {
				return err
			}
			c, err := newKubeClient(kubeconfig, kubecontext)
			if err != nil {
				return err
			}
			return adminResetPassword(cmd.Context(), c, cmd.OutOrStdout(), args[0], pw)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().StringVar(&kubecontext, "context", "", "Kubeconfig context to use")
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "Read the password from stdin instead of the terminal")
	return cmd
}

func newAdminCreateUserCmd() *cobra.Command {
	var kubeconfig, kubecontext, role string
	var stdin bool
	cmd := &cobra.Command{
		Use:   "create-user <email>",
		Short: "Create a user directly in the cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := auth.Role(role)
			if r != auth.RoleAdmin && r != auth.RoleMember {
				return fmt.Errorf("--role must be %q or %q", auth.RoleAdmin, auth.RoleMember)
			}
			pw, err := readPassword(cmd.InOrStdin(), cmd.ErrOrStderr(), stdin, "Password: ")
			if err != nil {
				return err
			}
			c, err := newKubeClient(kubeconfig, kubecontext)
			if err != nil {
				return err
			}
			return adminCreateUser(cmd.Context(), c, cmd.OutOrStdout(), args[0], pw, r)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: KUBECONFIG, then ~/.kube/config)")
	cmd.Flags().StringVar(&kubecontext, "context", "", "Kubeconfig context to use")
	cmd.Flags().StringVar(&role, "role", string(auth.RoleMember), "Role: admin or member")
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "Read the password from stdin instead of the terminal")
	return cmd
}

func adminResetPassword(ctx context.Context, c client.Client, out io.Writer, email, password string) error {
	if err := auth.NewNativeAuthProvider(c).UpdatePassword(ctx, email, password); err != nil {
		return fmt.Errorf("reset password for %s: %w", email, err)
	}
	fmt.Fprintf(out, "Password updated for %s; previously issued tokens are no longer valid.\n", email)
	return nil
}

func adminCreateUser(ctx context.Context, c client.Client, out io.Writer, email, password string, role auth.Role) error {
	if err := auth.NewNativeAuthProvider(c).CreateUser(ctx, email, password, role); err != nil {
		return fmt.Errorf("create user %s: %w", email, err)
	}
	fmt.Fprintf(out, "Created %s user %s.\n", role, email)
	return nil
}

// readPassword takes the password from stdin when asked, else prompts on the
// terminal without echo. A password never appears in argv, where it would
// land in shell history and `ps`.
func readPassword(in io.Reader, errOut io.Writer, fromStdin bool, prompt string) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", fmt.Errorf("empty password on stdin")
		}
		return pw, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("stdin is not a terminal; use --password-stdin")
	}
	fmt.Fprint(errOut, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(errOut)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	return string(b), nil
}
