package frigo

import (
	"context"
	"fmt"
	"strings"

	"github.com/roie/frigo/internal/git"
)

// resolveCommit resolves one safe commit-ish to one full OID in Frigo history.
// The caller holds the operation lock through the subsequent read.
func (w *Workspace) resolveCommit(ctx context.Context, revision string) (string, error) {
	if revision == "" || strings.ContainsAny(revision, "\r\n") || revision[0] == '-' {
		return "", fmt.Errorf("invalid frigo revision %q", revision)
	}
	oid, err := w.privateOutput(
		ctx,
		w.git.WithEnv("GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1"),
		"rev-parse", "--verify", "--quiet", revision+"^{commit}",
	)
	if err != nil {
		if code, ok := git.ExitCode(err); ok && code == 1 {
			return "", fmt.Errorf("invalid frigo revision %q", revision)
		}
		return "", fmt.Errorf("resolve frigo revision %q: %w", revision, err)
	}
	if oid == "" || strings.ContainsAny(oid, "\r\n") {
		return "", fmt.Errorf("frigo revision %q did not resolve to exactly one commit", revision)
	}
	return oid, nil
}
