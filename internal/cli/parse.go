package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

type commandOutput uint8

const (
	outputHuman commandOutput = iota
	outputPorcelainV1
	outputNUL
	outputPatch
	outputNameStatus
	outputBlob
)

type parsedCommand struct {
	name     string
	paths    []string
	revision string
	blobPath string
	message  string
	maxCount *int
	skip     *int
	output   commandOutput
	all      bool
	force    bool
	repair   bool
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
	case "status":
		return parseStatusArgs(args[1:])
	case "list", "ls":
		return parseListArgs(command, args[1:])
	case "diff":
		return parseDiffArgs(args[1:])
	case "log":
		return parseLogArgs(args[1:])
	case "show":
		return parseShowArgs(args[1:])
	case "doctor":
		return parseDoctorArgs(args[1:])
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

func parseStatusArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("status")
	var porcelain string
	var zero bool
	set.StringVar(&porcelain, "porcelain", "", "")
	set.BoolVar(&zero, "z", false, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor("status", err.Error())
	}
	paths := set.Args()
	if !flagWasSet(set, "porcelain") && !zero {
		if len(paths) != 0 {
			return parsedCommand{}, usageFor("status", "status paths require --porcelain=v1 -z")
		}
		return parsedCommand{name: "status"}, nil
	}
	if porcelain != "v1" {
		return parsedCommand{}, usageFor("status", "status supports only --porcelain=v1 -z")
	}
	if !zero {
		return parsedCommand{}, usageFor("status", "status --porcelain=v1 requires -z")
	}
	return parsedCommand{name: "status", paths: paths, output: outputPorcelainV1}, nil
}

func parseListArgs(command string, args []string) (parsedCommand, *usageError) {
	set := newFlagSet(command)
	var zero bool
	set.BoolVar(&zero, "z", false, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor(command, err.Error())
	}
	if len(set.Args()) != 0 {
		return parsedCommand{}, usageFor(command, fmt.Sprintf("%s does not accept paths", command))
	}
	var output commandOutput
	if zero {
		output = outputNUL
	}
	return parsedCommand{name: command, output: output}, nil
}

func parseDiffArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("diff")
	var patch bool
	set.BoolVar(&patch, "patch", false, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor("diff", err.Error())
	}
	var output commandOutput
	if patch {
		output = outputPatch
	}
	return parsedCommand{name: "diff", paths: set.Args(), output: output}, nil
}

func parseLogArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("log")
	var porcelain string
	var zero bool
	var maxCount int
	var skip int
	set.StringVar(&porcelain, "porcelain", "", "")
	set.BoolVar(&zero, "z", false, "")
	set.IntVar(&maxCount, "max-count", 0, "")
	set.IntVar(&skip, "skip", 0, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor("log", err.Error())
	}
	operands := set.Args()
	if len(operands) > 1 {
		return parsedCommand{}, usageFor("log", "log accepts at most one revision")
	}
	maxCountSet := flagWasSet(set, "max-count")
	skipSet := flagWasSet(set, "skip")
	if !flagWasSet(set, "porcelain") && !zero && !maxCountSet && !skipSet && len(operands) == 0 {
		return parsedCommand{name: "log"}, nil
	}
	if porcelain != "v1" {
		return parsedCommand{}, usageFor("log", "log supports only --porcelain=v1 -z")
	}
	if !zero {
		return parsedCommand{}, usageFor("log", "log --porcelain=v1 requires -z")
	}
	if maxCount < 0 || skip < 0 {
		return parsedCommand{}, usageFor("log", "log count options must be nonnegative")
	}
	command := parsedCommand{name: "log", output: outputPorcelainV1}
	if maxCountSet {
		command.maxCount = intPointerCopy(maxCount)
	}
	if skipSet {
		command.skip = intPointerCopy(skip)
	}
	if len(operands) == 1 {
		if err := validateRevision("log", operands[0]); err != nil {
			return parsedCommand{}, err
		}
		command.revision = operands[0]
	}
	return command, nil
}

func parseShowArgs(args []string) (parsedCommand, *usageError) {
	separator := len(args)
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}

	set := newFlagSet("show")
	var nameStatus bool
	var patch bool
	var zero bool
	set.BoolVar(&nameStatus, "name-status", false, "")
	set.BoolVar(&patch, "patch", false, "")
	set.BoolVar(&zero, "z", false, "")
	if err := set.Parse(args[:separator]); err != nil {
		return parsedCommand{}, usageFor("show", err.Error())
	}
	operands := set.Args()
	paths := []string(nil)
	if separator < len(args) {
		paths = append(paths, args[separator+1:]...)
	}

	if nameStatus || patch || zero {
		if nameStatus == patch {
			return parsedCommand{}, usageFor("show", "show requires exactly one of --name-status or --patch")
		}
		if nameStatus != zero {
			return parsedCommand{}, usageFor("show", "show --name-status requires -z and show --patch rejects -z")
		}
		if len(operands) != 1 {
			return parsedCommand{}, usageFor("show", "machine show requires exactly one revision")
		}
		if _, _, blob := splitBlobSpec(operands[0]); blob {
			return parsedCommand{}, usageFor("show", "cannot combine a blob revision with show options")
		}
		if err := validateRevision("show", operands[0]); err != nil {
			return parsedCommand{}, err
		}
		output := outputPatch
		if nameStatus {
			output = outputNameStatus
		}
		return parsedCommand{name: "show", revision: operands[0], paths: paths, output: output}, nil
	}

	if len(operands) > 1 {
		return parsedCommand{}, usageFor("show", "show accepts at most one revision; use -- before paths")
	}
	if len(operands) == 0 {
		return parsedCommand{name: "show", paths: paths}, nil
	}
	revision := operands[0]
	blobRevision, blobPath, blob := splitBlobSpec(revision)
	if blob {
		if separator < len(args) {
			return parsedCommand{}, usageFor("show", "show blob form does not accept path filters")
		}
		if blobPath == "" {
			return parsedCommand{}, usageFor("show", "show blob path cannot be empty")
		}
		if err := validateRevision("show", blobRevision); err != nil {
			return parsedCommand{}, err
		}
		return parsedCommand{name: "show", revision: blobRevision, blobPath: blobPath, output: outputBlob}, nil
	}
	if err := validateRevision("show", revision); err != nil {
		return parsedCommand{}, err
	}
	return parsedCommand{name: "show", revision: revision, paths: paths}, nil
}

func splitBlobSpec(value string) (revision, path string, ok bool) {
	separator := strings.IndexByte(value, ':')
	if separator <= 0 {
		return "", "", false
	}
	return value[:separator], value[separator+1:], true
}

func validateRevision(command, revision string) *usageError {
	switch {
	case revision == "":
		return usageFor(command, fmt.Sprintf("%s revision cannot be empty", command))
	case strings.ContainsAny(revision, "\r\n"):
		return usageFor(command, fmt.Sprintf("%s revision cannot contain a newline", command))
	case revision[0] == '-':
		return usageFor(command, fmt.Sprintf("%s revision cannot begin with '-'", command))
	default:
		return nil
	}
}

func flagWasSet(set *flag.FlagSet, name string) bool {
	setFlag := false
	set.Visit(func(current *flag.Flag) {
		if current.Name == name {
			setFlag = true
		}
	})
	return setFlag
}

func intPointerCopy(value int) *int {
	return &value
}

func parseDoctorArgs(args []string) (parsedCommand, *usageError) {
	set := newFlagSet("doctor")
	var repair bool
	set.BoolVar(&repair, "repair", false, "")
	if err := set.Parse(args); err != nil {
		return parsedCommand{}, usageFor("doctor", err.Error())
	}
	if len(set.Args()) != 0 {
		return parsedCommand{}, usageFor("doctor", "doctor does not accept arguments")
	}
	return parsedCommand{name: "doctor", repair: repair}, nil
}

func parseReleaseArgs(args []string) (parsedCommand, *usageError) {
	var all bool
	var force bool
	paths := make([]string, 0, len(args))
	options := true
	for _, arg := range args {
		if options {
			switch arg {
			case "--":
				options = false
				continue
			case "-all", "--all":
				all = true
				continue
			case "-force", "--force":
				force = true
				continue
			default:
				if len(arg) > 0 && arg[0] == '-' {
					return parsedCommand{}, usageFor("release", fmt.Sprintf("flag provided but not defined: %s", arg))
				}
			}
		}
		paths = append(paths, arg)
	}
	switch {
	case all && len(paths) > 0:
		return parsedCommand{}, usageFor("release", "release --all does not accept paths")
	case all:
		return parsedCommand{name: "release", all: true, force: force}, nil
	case len(paths) == 0:
		return parsedCommand{}, usageFor("release", "release requires --all or at least one path")
	default:
		return parsedCommand{name: "release", paths: paths, force: force}, nil
	}
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
	options := true
	expectMessage := false
	for _, arg := range args {
		if expectMessage {
			expanded = append(expanded, arg)
			expectMessage = false
			continue
		}
		if options {
			switch arg {
			case "--":
				options = false
			case "-m":
				expectMessage = true
			case "-am":
				expanded = append(expanded, "-a", "-m")
				expectMessage = true
				continue
			default:
				if len(arg) == 0 || arg[0] != '-' {
					options = false
				}
			}
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
