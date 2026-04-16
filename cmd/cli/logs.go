package main

import (
	"bufio"
	"fmt"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <app>",
		Short: "Stream logs from an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClientFromConfig()
			if err != nil {
				return err
			}
			resp, err := c.do("GET", "/api/apps/"+args[0]+"/logs?follow=true", nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return fmt.Errorf("API error %d", resp.StatusCode)
			}
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				fmt.Println(scanner.Text())
			}
			return scanner.Err()
		},
	}
}
