package cli

import (
	"fmt"
	"io"
	"strings"
)

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `Usage: frigo <command> [options]
Commands: add, release, status, list, diff, commit, log, show, restore, doctor, help
Run 'frigo help' for detailed help.`)
}

func printHelp(output io.Writer) {
	fmt.Fprintln(output, `frigo keeps local project files without adding them to your main Git history.

Usage:
  frigo add [--] <path>...
  frigo release [--all] [--force] [--] <path>...
  frigo status
  frigo status --porcelain=v1 -z [--] [<path>...]
  frigo list | frigo ls
  frigo list -z | frigo ls -z
  frigo diff [--] [<path>...]
  frigo diff --patch [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo log --porcelain=v1 -z [--max-count=<n>] [--skip=<n>] [<revision>]
  frigo show [<revision>] [-- <path>...]
  frigo show --name-status -z <revision> [-- <path>...]
  frigo show --patch <revision> [-- <path>...]
  frigo show <revision>:<path>
  frigo restore [--] <path>...
  frigo doctor [--repair]

Commands:
  add      Assign existing untracked paths to frigo.
  release  Release exact ownership in the current worktree without deleting files or history; other Git ignore rules may still hide it, or every owned root with --all.
  status   Show main-repository and frigo working-tree status.
  list     List exact ownership roots; ls is an alias.
  diff     Show owned changes against frigo HEAD.
  commit   Commit selected paths, or every owned change with -a.
  log      Show frigo commit history.
  show     Show one frigo commit and its patch.
  restore  Restore saved owned paths from frigo HEAD.
  doctor   Diagnose metadata, or apply bounded repairs with --repair.

Notes:
  doctor --repair prints a complete repair plan before mutation.
  release --all applies only to the current worktree.
  Porcelain and -z forms write machine-readable bytes without headings.

Use -- before paths beginning with '-'. frigo has no persistent staging area.`)
}

func printCommandUsage(output io.Writer, command string) {
	switch command {
	case "add":
		fmt.Fprintln(output, "Usage: frigo add [--] <path>...")
	case "release":
		fmt.Fprintln(output, "Usage: frigo release [--all] [--force] [--] <path>...")
		fmt.Fprintln(output, "release --all applies only to the current worktree.")
	case "status":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo status")
		fmt.Fprintln(output, "  frigo status --porcelain=v1 -z [--] [<path>...]")
	case "list":
		fmt.Fprintln(output, "Usage: frigo list [-z]")
	case "ls":
		fmt.Fprintln(output, "Usage: frigo ls [-z]")
	case "diff":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo diff [--] [<path>...]")
		fmt.Fprintln(output, "  frigo diff --patch [--] [<path>...]")
	case "commit":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo commit -m <message> [--] <path>...")
		fmt.Fprintln(output, "  frigo commit -a -m <message>")
		fmt.Fprintln(output, "  frigo commit -am <message>")
	case "log":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo log")
		fmt.Fprintln(output, "  frigo log --porcelain=v1 -z [--max-count=<n>] [--skip=<n>] [<revision>]")
	case "show":
		fmt.Fprintln(output, "Usage:")
		fmt.Fprintln(output, "  frigo show [<revision>] [-- <path>...]")
		fmt.Fprintln(output, "  frigo show --name-status -z <revision> [-- <path>...]")
		fmt.Fprintln(output, "  frigo show --patch <revision> [-- <path>...]")
		fmt.Fprintln(output, "  frigo show <revision>:<path>")
	case "restore":
		fmt.Fprintln(output, "Usage: frigo restore [--] <path>...")
	case "doctor":
		fmt.Fprintln(output, "Usage: frigo doctor [--repair]")
		fmt.Fprintln(output, "doctor --repair prints a complete repair plan before mutation.")
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
