package cli

import (
	"fmt"
	"io"
	"strings"
)

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  frigo
  frigo add [--] <path>...
  frigo release [--force] [--] <path>...
  frigo status
  frigo list
  frigo ls
  frigo diff [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo restore [--] <path>...
  frigo help`)
}

func printCommandUsage(output io.Writer, command string) {
	switch command {
	case "add":
		fmt.Fprintln(output, "Usage: frigo add [--] <path>...")
	case "release":
		fmt.Fprintln(output, "Usage: frigo release [--force] [--] <path>...")
	case "status":
		fmt.Fprintln(output, "Usage: frigo status")
	case "list":
		fmt.Fprintln(output, "Usage: frigo list")
	case "ls":
		fmt.Fprintln(output, "Usage: frigo ls")
	case "diff":
		fmt.Fprintln(output, "Usage: frigo diff [--] [<path>...]")
	case "commit":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo commit -m <message> [--] <path>...")
		fmt.Fprintln(output, "  frigo commit -a -m <message>")
		fmt.Fprintln(output, "  frigo commit -am <message>")
	case "log":
		fmt.Fprintln(output, "Usage: frigo log")
	case "restore":
		fmt.Fprintln(output, "Usage: frigo restore [--] <path>...")
	}
}

func printIndentedStatus(output io.Writer, status string) {
	if status == "" {
		fmt.Fprintln(output, "  clean")
		return
	}
	for _, line := range strings.Split(status, "\n") {
		fmt.Fprintf(output, "  %s\n", line)
	}
}
