package main

import (
	"os"

	"github.com/artbegolli/anvilmcp/internal/cmd"
)

func main() {
	root := cmd.NewRootCommand()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
