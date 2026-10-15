// Command dialga imports and exports SQL Server databases on Amazon
// RDS through S3. Bootstrap/spike stage: "serve" runs the local API for the
// desktop shell; the CLI commands of the plan land in v0.1.0 and later.
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
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}
	switch args[0] {
	case "version", "--version", "-v":
		_, _ = fmt.Fprintf(stdout, "dialga %s\n", version)
		return 0
	case "serve":
		return serve(stdin, stdout, stderr)
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "dialga: unknown or not yet implemented command %q\n", args[0])
		usage(stderr)
		return 1
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage: dialga <command>

Commands:
  serve     Run the local API for the desktop shell (session token on stdin)
  version   Print the version
  help      Show this help
`)
}
