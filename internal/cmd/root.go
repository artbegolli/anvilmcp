package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "anvilmcp",
		Short: "Anvil MCP Server - shapes solutions in Kubernetes dev containers",
		Long: `Anvil MCP Server enables chat interfaces to interact with live development
container sessions through the Model Context Protocol.`,
		Version: "0.1.0",
	}

	root.AddCommand(NewServeCommand())
	root.AddCommand(NewPolicyCommand())
	root.AddCommand(NewHistoryCommand())

	return root
}

func notImplemented(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%q is not yet implemented", cmd.CommandPath())
		},
	}
}
