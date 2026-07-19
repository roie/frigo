package frigo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/roie/frigo/internal/git"
)

var (
	createTemporaryIndex = os.CreateTemp
	closeTemporaryIndex  = func(file *os.File) error { return file.Close() }
	removeTemporaryIndex = os.Remove
)

func (w *Workspace) withTemporaryIndex(ctx context.Context, intentPaths []string, fn func(client git.Client) error) (returnErr error) {
	file, err := createTemporaryIndex(w.repo.FrigoDir, "temporary-index-*")
	if err != nil {
		return fmt.Errorf("allocate temporary index: %w", err)
	}
	name := file.Name()
	defer func() {
		if err := removeTemporaryIndex(name); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary index: %w", err))
		}
	}()

	if err := closeTemporaryIndex(file); err != nil {
		return fmt.Errorf("close temporary index: %w", err)
	}
	if err := removeTemporaryIndex(name); err != nil {
		return fmt.Errorf("remove temporary index: %w", err)
	}

	client := w.git.WithEnv("GIT_INDEX_FILE="+name, "GIT_ATTR_NOSYSTEM=1")
	if err := w.seedIndex(ctx, client); err != nil {
		return err
	}
	if len(intentPaths) > 0 {
		args := append([]string{"add", "--force", "-N", "--"}, intentPaths...)
		if _, err := w.privateOutput(ctx, client, args...); err != nil {
			return err
		}
	}
	return fn(client)
}

func (w *Workspace) seedIndex(ctx context.Context, client git.Client) error {
	hasHead, err := w.hasHead(ctx)
	if err != nil {
		return err
	}
	args := []string{"read-tree", "--empty"}
	if hasHead {
		args = []string{"read-tree", "HEAD"}
	}
	if _, err := w.privateOutput(ctx, client, args...); err != nil {
		return fmt.Errorf("seed temporary index: %w", err)
	}
	return nil
}

func (w *Workspace) hasHead(ctx context.Context) (bool, error) {
	_, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return true, nil
	}
	if code, ok := git.ExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect frigo history: %w", err)
}
