package main

import (
	"context"
	"fmt"
	"os"

	"github.com/joansebgarcia/amber/cmd"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	if err := cmd.NewRoot(version).ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
