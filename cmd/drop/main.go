//go:build linux

// Command drop is the Drop server and client in one binary. Drop runs on
// Linux only (amd64 and arm64); main_other.go refuses every other system.
package main

import (
	"os"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/client"
)

func main() {
	os.Exit(client.Run(os.Args[1:]))
}
