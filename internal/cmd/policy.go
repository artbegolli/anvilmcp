package cmd

import (
	"github.com/spf13/cobra"
)

func NewPolicyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage session policy",
	}

	cmd.AddCommand(notImplemented("init", "Initialize default policy file"))
	cmd.AddCommand(notImplemented("show", "Show effective policy"))
	cmd.AddCommand(notImplemented("test", "Test if operation would be allowed"))

	return cmd
}
