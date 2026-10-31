// Command vecnet is the vecnet command line client (PROTOCOL §13.7); all
// behaviour lives in internal/cli.
package main

import (
	"os"

	"github.com/XertroV/vecnet/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
