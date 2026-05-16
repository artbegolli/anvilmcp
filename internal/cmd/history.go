package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewHistoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Manage command history",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recent executions",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("history list - TODO")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show a specific execution",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("history show - TODO")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "tail",
		Short: "Tail history as it happens",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("history tail - TODO")
			return nil
		},
	})

	return cmd
}
