//go:build !linux

// Command drop is the Drop server and client in one binary. Drop runs on
// Linux only (amd64 and arm64); a build for any other system only refuses.
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() {
	fmt.Fprintf(os.Stderr, "drop: %s/%s is not supported; Drop runs on Linux only (amd64 and arm64)\n", runtime.GOOS, runtime.GOARCH)
	os.Exit(1)
}
