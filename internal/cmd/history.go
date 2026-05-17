package cmd

import (
	"github.com/spf13/cobra"
)

func NewHistoryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Manage command history",
	}

	cmd.AddCommand(notImplemented("list", "List recent executions"))
	cmd.AddCommand(notImplemented("show", "Show a specific execution"))
	cmd.AddCommand(notImplemented("tail", "Tail history as it happens"))

	return cmd
}
