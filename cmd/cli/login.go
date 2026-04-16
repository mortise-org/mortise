package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var serverURL string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with a Mortise server",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)

			if serverURL == "" {
				fmt.Print("Server URL: ")
				line, _ := reader.ReadString('\n')
				serverURL = strings.TrimSpace(line)
			}

			fmt.Print("Email: ")
			email, _ := reader.ReadString('\n')
			email = strings.TrimSpace(email)

			fmt.Print("Password: ")
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return fmt.Errorf("reading password: %w", err)
			}

			c := &Client{
				BaseURL:    serverURL,
				HTTPClient: defaultHTTPClient(),
			}

			var resp struct {
				Token string `json:"token"`
			}
			err = c.doJSON("POST", "/api/auth/login", map[string]string{
				"email":    email,
				"password": string(pw),
			}, &resp)
			if err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			if err := saveConfig(&Config{
				ServerURL: serverURL,
				Token:     resp.Token,
			}); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Println("Logged in successfully.")
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "Mortise server URL")
	return cmd
}
