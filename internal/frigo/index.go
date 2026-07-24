package frigo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

func (w *Workspace) comparisonOID(ctx context.Context, base historyBase) (string, error) {
	if base.Exists {
		return base.OID, nil
	}
	oid, err := w.git.WithEnv("GIT_ATTR_NOSYSTEM=1").OutputWithInput(
		ctx,
		w.repo.Root,
		"",
		"--git-dir="+w.repo.HistoryDir,
		"--work-tree="+w.repo.Root,
		"hash-object",
		"-t",
		"tree",
		"--stdin",
	)
	if err != nil {
		return "", fmt.Errorf("resolve empty frigo history base: %w", err)
	}
	return oid, nil
}

// statusAtOID preserves Git's porcelain formatting while replacing the private
// repository's symbolic HEAD with a temporary detached HEAD at oid.
func (w *Workspace) statusAtOID(ctx context.Context, client git.Client, oid string, args ...string) (output string, returnErr error) {
	gitDir, err := os.MkdirTemp(w.repo.FrigoDir, "temporary-git-dir-*")
	if err != nil {
		return "", fmt.Errorf("allocate pinned frigo status directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(gitDir); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove pinned frigo status directory: %w", err))
		}
	}()

	for filename, contents := range map[string]string{
		"commondir": w.repo.HistoryDir + "\n",
		"HEAD":      oid + "\n",
	} {
		if err := os.WriteFile(filepath.Join(gitDir, filename), []byte(contents), 0o600); err != nil {
			return "", fmt.Errorf("write pinned frigo %s: %w", filename, err)
		}
	}
	commandArgs := append([]string{"--git-dir=" + gitDir, "--work-tree=" + w.repo.Root}, args...)
	output, err = client.Output(ctx, w.repo.Root, commandArgs...)
	return output, err
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
