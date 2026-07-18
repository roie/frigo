package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesParentsAndWritesData(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "nested", "registry.json")
	if err := Write(filename, []byte("hello"), 0o640); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("file contents = %q, want %q", got, "hello")
	}

	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("file mode = %v, want %v", got, want)
	}
}
