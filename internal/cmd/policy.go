package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewPolicyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage session policy",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Initialize default policy file",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("policy init - TODO")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show effective policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("policy show - TODO")
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Test if operation would be allowed",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("policy test - TODO")
			return nil
		},
	})

	return cmd
}
