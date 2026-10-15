// Command dialga imports and exports SQL Server databases on Amazon
// RDS through S3. This is the bootstrap skeleton: only "version" works; the
// other commands of the plan land in v0.1.0 and later.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is overridden at build time by ci/build-engine.sh
// (-ldflags "-X main.version=<version>").
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "dialga %s\n", version)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "dialga: unknown or not yet implemented command %q\n", args[0])
		usage(stderr)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: dialga <command>

Commands:
  version   Print the version
  help      Show this help
`)
}
