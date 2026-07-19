package cli

import (
	"fmt"
	"io"
	"strings"
)

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `Usage: frigo <command> [options]
Commands: add, release, status, list, diff, commit, log, restore, help
Run 'frigo help' for detailed help.`)
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `frigo keeps selected paths in a separate local Git history.

Usage:
  frigo add [--] <path>...
  frigo release [--force] [--] <path>...
  frigo status
  frigo list | frigo ls
  frigo diff [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo restore [--] <path>...

Commands:
  add      Assign existing untracked paths to frigo.
  release  Release exact ownership without deleting files or history.
  status   Show main-repository and frigo working-tree status.
  list     List exact ownership roots; ls is an alias.
  diff     Show owned changes against frigo HEAD.
  commit   Commit selected paths, or every owned change with -a.
  log      Show frigo commit history.
  restore  Restore saved owned paths from frigo HEAD.

Use -- before paths beginning with '-'. frigo has no persistent staging area.`)
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
