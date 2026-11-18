// Command _cli-fixture runs only synthetic test subprocesses.
package main

import (
	"github.com/XertroV/vecnet/internal/testfixture/cliprogram"
	"os"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	spec, err := cliprogram.ReadSpec(exe)
	if err != nil {
		panic(err)
	}
	os.Exit(cliprogram.Run(spec))
}
