package cmd

import (
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
