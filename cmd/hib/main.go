// Command hib is Hibernaut's laptop CLI.
package main

import (
	"fmt"
	"os"

	"github.com/solev/hibernaut/internal/version"
)

const usage = `usage: hib <command>

commands:
  version   print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println("hib", version.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "hib: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
