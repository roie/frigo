package frigo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"frigo/internal/git"
	"frigo/internal/registry"
)

type CommitOptions struct {
	Message string
	All     bool
	Paths   []string
}

type CommitResult struct {
	Committed bool
	Commit    string
}

func (w *Workspace) Commit(ctx context.Context, options CommitOptions) (CommitResult, error) {
	if strings.TrimSpace(options.Message) == "" {
		return CommitResult{}, errors.New("commit message cannot be empty")
	}
	if options.All && len(options.Paths) > 0 {
		return CommitResult{}, errors.New("cannot combine -a with commit paths")
	}
	if !options.All && len(options.Paths) == 0 {
		return CommitResult{}, errors.New("no paths specified; use -a to commit all owned changes")
	}

	owned, err := w.loadRegistry()
	if err != nil {
		return CommitResult{}, err
	}
	paths, err := w.commitPaths(options, owned)
	if err != nil {
		return CommitResult{}, err
	}

	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return CommitResult{}, err
	}

	var result CommitResult
	if err := w.withTemporaryIndex(ctx, intentPaths, func(client git.Client) error {
		if len(paths) > 0 {
			args := append([]string{"add", "--force", "--all", "--"}, paths...)
			if _, err := w.privateOutput(ctx, client, args...); err != nil {
				return fmt.Errorf("stage frigo files: %w", err)
			}
		}

		_, diffErr := w.privateOutput(ctx, client, "diff", "--cached", "--quiet", "--exit-code")
		if diffErr == nil {
			result = CommitResult{Committed: false}
			return nil
		}
		if code, ok := git.ExitCode(diffErr); !ok || code != 1 {
			return fmt.Errorf("inspect staged frigo changes: %w", diffErr)
		}

		tree, err := w.privateOutput(ctx, client, "write-tree")
		if err != nil {
			return fmt.Errorf("write frigo tree: %w", err)
		}
		commitArgs := []string{"commit-tree", tree, "-m", options.Message}
		hasHead, err := w.hasHead(ctx)
		if err != nil {
			return err
		}
		if hasHead {
			parent, err := w.privateOutput(ctx, client, "rev-parse", "HEAD")
			if err != nil {
				return fmt.Errorf("read frigo parent commit: %w", err)
			}
			commitArgs = []string{"commit-tree", tree, "-p", parent, "-m", options.Message}
		}
		commitClient, err := w.commitClient(ctx, client)
		if err != nil {
			return err
		}
		commit, err := w.privateOutput(ctx, commitClient, commitArgs...)
		if err != nil {
			return fmt.Errorf("create frigo commit: %w", err)
		}
		if _, err := w.privateOutput(ctx, client, "update-ref", "HEAD", commit); err != nil {
			return fmt.Errorf("update frigo HEAD: %w", err)
		}
		shortCommit, err := w.privateOutput(ctx, client, "rev-parse", "--short", "HEAD")
		if err != nil {
			return fmt.Errorf("read frigo commit: %w", err)
		}
		result = CommitResult{Committed: true, Commit: shortCommit}
		return nil
	}); err != nil {
		return CommitResult{}, err
	}
	return result, nil
}

func (w *Workspace) commitPaths(options CommitOptions, owned registry.Registry) ([]string, error) {
	if options.All {
		return append([]string(nil), owned.Paths...), nil
	}
	return w.resolveScopedPaths(options.Paths, owned)
}

type gitIdentity struct {
	Name  string
	Email string
}

func (w *Workspace) commitClient(ctx context.Context, client git.Client) (git.Client, error) {
	author, err := w.mainIdentity(ctx, "GIT_AUTHOR_IDENT")
	if err != nil {
		return git.Client{}, fmt.Errorf("read main Git author identity: %w", err)
	}
	committer, err := w.mainIdentity(ctx, "GIT_COMMITTER_IDENT")
	if err != nil {
		return git.Client{}, fmt.Errorf("read main Git committer identity: %w", err)
	}
	return client.WithEnv(
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_COMMITTER_NAME="+committer.Name,
		"GIT_COMMITTER_EMAIL="+committer.Email,
	), nil
}

func (w *Workspace) mainIdentity(ctx context.Context, key string) (gitIdentity, error) {
	value, err := w.git.Output(ctx, "", "-C", w.repo.Root, "var", key)
	if err != nil {
		return gitIdentity{}, err
	}
	name, email, err := parseGitIdent(value)
	if err != nil {
		return gitIdentity{}, fmt.Errorf("parse %s: %w", key, err)
	}
	return gitIdentity{Name: name, Email: email}, nil
}

func parseGitIdent(value string) (name, email string, err error) {
	value = strings.TrimSpace(value)
	start := strings.LastIndex(value, " <")
	end := strings.LastIndex(value, ">")
	if start < 0 || end <= start+2 {
		return "", "", fmt.Errorf("invalid ident %q", value)
	}
	name = strings.TrimSpace(value[:start])
	email = value[start+2 : end]
	if name == "" || email == "" {
		return "", "", fmt.Errorf("invalid ident %q", value)
	}
	return name, email, nil
}
