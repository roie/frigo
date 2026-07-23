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

type historyBase struct {
	OID    string
	Exists bool
}

func (w *Workspace) withTemporaryIndexAt(ctx context.Context, base historyBase, intentPaths []string, fn func(client git.Client) error) (returnErr error) {
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
	args := []string{"read-tree", "--empty"}
	if base.Exists {
		args = []string{"read-tree", base.OID}
	}
	if _, err := w.privateOutput(ctx, client, args...); err != nil {
		return fmt.Errorf("seed temporary index: %w", err)
	}
	if len(intentPaths) > 0 {
		args := append([]string{"add", "--force", "-N", "--"}, intentPaths...)
		if _, err := w.privateOutput(ctx, client, args...); err != nil {
			return err
		}
	}
	return fn(client)
}

func (w *Workspace) resolveHistoryBase(ctx context.Context) (historyBase, error) {
	oid, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return historyBase{OID: oid, Exists: true}, nil
	}
	if code, ok := git.ExitCode(err); ok && code == 1 {
		return historyBase{}, nil
	}
	return historyBase{}, fmt.Errorf("inspect frigo history: %w", err)
}

func (w *Workspace) hasHead(ctx context.Context) (bool, error) {
	base, err := w.resolveHistoryBase(ctx)
	return base.Exists, err
}
