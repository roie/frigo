package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	gitpkg "frigo/internal/git"
	"frigo/internal/repository"
	"frigo/internal/frigo"
)

var minimumGitVersion = gitpkg.Version{Major: 2, Minor: 23, Patch: 0}

// Run executes the frigo command using the process working directory.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "frigo: determine current directory: %v\n", err)
		return 1
	}
	return runAt(ctx, args, stdin, stdout, stderr, cwd, gitpkg.Client{Path: "git"})
}

func runAt(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, cwd string, client gitpkg.Client) int {
	_ = stdin
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}
	if args[0] == "help" {
		if len(args) == 1 {
			printUsage(stdout)
			return 0
		}
		return printUsageError(stderr, &usageError{message: "help does not accept arguments", general: true})
	}
	if args[0] == "--help" {
		if len(args) == 1 {
			printUsage(stdout)
			return 0
		}
		return printUsageError(stderr, &usageError{message: "--help does not accept arguments", general: true})
	}

	parsed, usageErr := parseArgs(args)
	if usageErr != nil {
		return printUsageError(stderr, usageErr)
	}

	if err := gitpkg.CheckMinimum(ctx, client, minimumGitVersion); err != nil {
		return printError(stderr, err)
	}
	repo, err := repository.Discover(ctx, client, cwd)
	if err != nil {
		return printError(stderr, err)
	}
	workspace := frigo.NewWorkspace(repo, client, cwd)

	switch parsed.name {
	case "add":
		result, err := workspace.Add(ctx, parsed.paths)
		if err != nil {
			return printError(stderr, err)
		}
		for _, path := range result.Added {
			fmt.Fprintf(stdout, "added %s\n", path)
		}
		for _, path := range parsed.paths {
			if covering, ok := result.AlreadyOwned[path]; ok {
				if covering == path {
					fmt.Fprintf(stdout, "already owned %s\n", path)
				} else {
					fmt.Fprintf(stdout, "already owned %s via %s\n", path, covering)
				}
			}
		}
	case "release":
		result, err := workspace.Release(ctx, parsed.paths, parsed.force)
		if err != nil {
			return printError(stderr, err)
		}
		for _, path := range result.Released {
			fmt.Fprintf(stdout, "released %s\n", path)
		}
	case "status":
		mainStatus, err := client.Output(ctx, repo.Root, "status", "--short", "--untracked-files=all", "--")
		if err != nil {
			return printError(stderr, fmt.Errorf("read main status: %w", err))
		}
		privateStatus, err := workspace.Status(ctx, nil)
		if err != nil {
			return printError(stderr, err)
		}
		fmt.Fprintln(stdout, "main")
		printIndentedStatus(stdout, mainStatus)
		fmt.Fprintln(stdout, "frigo")
		printIndentedStatus(stdout, privateStatus)
	case "list", "ls":
		paths, err := workspace.List(ctx, nil)
		if err != nil {
			return printError(stderr, err)
		}
		for _, path := range paths {
			fmt.Fprintln(stdout, path)
		}
	case "diff":
		output, err := workspace.Diff(ctx, parsed.paths)
		if err != nil {
			return printError(stderr, err)
		}
		if output == "" {
			fmt.Fprintln(stdout, "no changes")
		} else {
			fmt.Fprintln(stdout, output)
		}
	case "commit":
		result, err := workspace.Commit(ctx, frigo.CommitOptions{
			Message: parsed.message,
			All:     parsed.all,
			Paths:   parsed.paths,
		})
		if err != nil {
			return printError(stderr, err)
		}
		if !result.Committed {
			fmt.Fprintln(stdout, "nothing to commit")
		} else {
			fmt.Fprintf(stdout, "committed %s\n", result.Commit)
		}
	case "log":
		output, err := workspace.Log(ctx)
		if err != nil {
			return printError(stderr, err)
		}
		fmt.Fprintln(stdout, output)
	case "restore":
		paths, err := workspace.Restore(ctx, parsed.paths)
		if err != nil {
			return printError(stderr, err)
		}
		for _, path := range paths {
			fmt.Fprintf(stdout, "restored %s\n", path)
		}
	}
	return 0
}

func printUsageError(stderr io.Writer, err *usageError) int {
	fmt.Fprintf(stderr, "frigo: %s\n", err.message)
	fmt.Fprintln(stderr)
	if err.general {
		printUsage(stderr)
	} else {
		printCommandUsage(stderr, err.command)
	}
	return 2
}

func printError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "frigo: %v\n", err)
	return 1
}
