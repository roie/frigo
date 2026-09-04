package cli

import (
	"strings"
	"testing"
)

func TestEmptyPorcelainVersionFailsBeforeRepositoryDiscovery(t *testing.T) {
	for _, command := range []string{"status", "log"} {
		for _, args := range [][]string{{command, "--porcelain="}, {command, "--porcelain=", "-z"}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				root := t.TempDir()
				got := invoke(t, root, args...)
				if got.code != 2 || got.stdout != "" {
					t.Fatalf("%q: %+v, want usage failure and empty stdout", args, got)
				}
			})
		}
	}
}
