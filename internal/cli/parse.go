package cli

import (
	"flag"
	"fmt"
	"io"
)

type parsedCommand struct {
	name    string
	paths   []string
	message string
	all     bool
	force   bool
}

type usageError struct {
	command string
	message string
	general bool
}

func parseArgs(args []string) (parsedCommand, *usageError) {
	command := args[0]
	switch command {
	case "add":
		return parsePathCommand(command, args[1:], true)
	case "release":
		return parseReleaseArgs(args[1:])
	case "status", "list", "ls", "log":
		return parseNoArgCommand(command, args[1:])
	case "diff":
		return parsePathCommand(command, args[1:], false)
	case "commit":
		return parseCommitArgs(args[1:])
	case "restore":
		return parsePathCommand(command, args[1:], true)
	default:
		return parsedCommand{}, &usageError{
			message: fmt.Sprintf("unknown command %q", command),
			general: true,
		}
	}
}

func parsePathCommand(command string, args []string, requirePaths bool) (parsedCommand, *usageError) {
	set := newFlagSet(command)
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor(command, err.Error())
	}
	paths := set.Args()
	if requirePaths && len(paths) == 0 {
		return parsedCommand{}, usageFor(command, fmt.Sprintf("%s requires at least one path", command))
	}
	return parsedCommand{name: command, paths: paths}, nil
}

func parseNoArgCommand(command string, args []string) (parsedCommand, *usageError) {
	set := newFlagSet(command)
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor(command, err.Error())
	}
	if len(set.Args()) != 0 {
		return parsedCommand{}, usageFor(command, fmt.Sprintf("%s does not accept arguments", command))
	}
	return parsedCommand{name: command}, nil
}

func parseReleaseArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("release")
	var force bool
	set.BoolVar(&force, "force", false, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor("release", err.Error())
	}
	paths := set.Args()
	if len(paths) == 0 {
		return parsedCommand{}, usageFor("release", "release requires at least one path")
	}
	return parsedCommand{name: "release", paths: paths, force: force}, nil
}

func parseCommitArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("commit")
	var all bool
	var message string
	set.BoolVar(&all, "a", false, "")
	set.StringVar(&message, "m", "", "")
	if err := set.Parse(expandCommitArgs(args)); err != nil {
		return parsedCommand{}, usageFor("commit", err.Error())
	}
	paths := set.Args()
	switch {
	case message == "":
		return parsedCommand{}, usageFor("commit", "commit requires -m <message>")
	case all && len(paths) > 0:
		return parsedCommand{}, usageFor("commit", "cannot combine -a with commit paths")
	case !all && len(paths) == 0:
		return parsedCommand{}, usageFor("commit", "no paths specified; use -a to commit all owned changes")
	default:
		return parsedCommand{name: "commit", paths: paths, message: message, all: all}, nil
	}
}

func expandCommitArgs(args []string) []string {
	expanded := make([]string, 0, len(args)+1)
	for _, arg := range args {
		if arg == "-am" {
			expanded = append(expanded, "-a", "-m")
			continue
		}
		expanded = append(expanded, arg)
	}
	return expanded
}

func newFlagSet(command string) *flag.FlagSet {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

func usageFor(command, message string) *usageError {
	return &usageError{command: command, message: message}
}
