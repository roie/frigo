package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateIDReturns32LowercaseHex(t *testing.T) {
	id, err := GenerateID()
	if err != nil {
		t.Fatalf("GenerateID() error = %v", err)
	}
	if got, want := len(id), 32; got != want {
		t.Fatalf("GenerateID() length = %d, want %d", got, want)
	}
	if id != strings.ToLower(id) {
		t.Fatalf("GenerateID() = %q, want lowercase hex", id)
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("GenerateID() = %q, want lowercase hex", id)
		}
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "manifest.json")
	data := []byte(`{"version":1,"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","worktreePath":"worktree","lockOwned":false,"extra":true}`)
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(filename)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsInvalidUTF8(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "manifest.json")
	data := []byte{'{', '"', 'v', 'e', 'r', 's', 'i', 'o', 'n', '"', ':', '1', ',', '"', 'i', 'd', '"', ':', '"', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', 'a', '"', ',', '"', 'w', 'o', 'r', 'k', 't', 'r', 'e', 'e', 'P', 'a', 't', 'h', '"', ':', '"', 'b', 'a', 'd', '-', 0xff, '"', ',', '"', 'l', 'o', 'c', 'k', 'O', 'w', 'n', 'e', 'd', '"', ':', 'f', 'a', 'l', 's', 'e', '}'}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(filename)
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("Load() error = %v, want UTF-8 rejection", err)
	}
}

func TestSaveAndLoadPreserveReplacementRune(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "state", "manifest.json")
	original := Manifest{
		Version:      CurrentVersion,
		ID:           strings.Repeat("a", 32),
		WorktreePath: "bad-�.md",
		LockOwned:    true,
	}
	if err := Save(filename, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded != original {
		t.Fatalf("Load() = %#v, want %#v", loaded, original)
	}
}

func TestSavePointerWritesExactFormatAndRoundTrips(t *testing.T) {
	testID := strings.Repeat("b", 32)
	filename := filepath.Join(t.TempDir(), "nested", "worktree-id")
	if err := SavePointer(filename, testID); err != nil {
		t.Fatalf("SavePointer() error = %v", err)
	}

	got, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != testID+"\n" {
		t.Fatalf("pointer file = %q, want %q", got, testID+"\\n")
	}

	loaded, err := LoadPointer(filename)
	if err != nil {
		t.Fatalf("LoadPointer() error = %v", err)
	}
	if loaded != testID {
		t.Fatalf("LoadPointer() = %q, want %q", loaded, testID)
	}
}

func TestLoadPointerRejectsMalformedPointers(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "missing newline", data: strings.Repeat("a", 32)},
		{name: "extra data", data: strings.Repeat("a", 32) + "\nextra"},
		{name: "uppercase", data: strings.ToUpper(strings.Repeat("a", 32)) + "\n"},
		{name: "short", data: "abc\n"},
		{name: "bad character", data: strings.Repeat("g", 32) + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "worktree-id")
			if err := os.WriteFile(filename, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := LoadPointer(filename)
			if err == nil {
				t.Fatal("LoadPointer() error = nil, want rejection")
			}
		})
	}
}
