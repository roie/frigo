package ignore

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/roie/frigo/internal/atomicfile"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
)

const (
	startMarker = "# >>> frigo >>>"
	endMarker   = "# <<< frigo <<<"
)

// LiteralPattern converts a normalized root-relative path into a literal root-anchored ignore pattern.
func LiteralPattern(candidate string) (string, error) {
	if candidate == "" || candidate == "." || strings.HasPrefix(candidate, "/") {
		return "", fmt.Errorf("invalid relative path %q", candidate)
	}
	if strings.ContainsAny(candidate, "\r\n") {
		return "", fmt.Errorf("path contains a newline: %q", candidate)
	}
	clean := path.Clean(candidate)
	if clean != candidate || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid relative path %q", candidate)
	}

	var builder strings.Builder
	builder.Grow(len(candidate) + 1)
	builder.WriteByte('/')
	for i := 0; i < len(candidate); i++ {
		char := candidate[i]
		if strings.ContainsRune(`\*?[] `, rune(char)) {
			builder.WriteByte('\\')
		}
		builder.WriteByte(char)
	}
	return builder.String(), nil
}

// Sync rewrites frigo's managed section in the common info/exclude file.
func Sync(repo repository.Repository, owned registry.Registry) error {
	paths, err := unionPaths(repo, owned)
	if err != nil {
		return fmt.Errorf("collect frigo exclude paths: %w", err)
	}

	existing, err := os.ReadFile(repo.ExcludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read Git exclude file: %w", err)
	}

	output, err := rewrite(existing, paths)
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(repo.ExcludePath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicfile.Write(repo.ExcludePath, output, mode); err != nil {
		return fmt.Errorf("write Git exclude file: %w", err)
	}
	return nil
}

func unionPaths(repo repository.Repository, owned registry.Registry) ([]string, error) {
	seen := make(map[string]struct{}, len(owned.Paths))
	for _, candidate := range owned.Paths {
		seen[candidate] = struct{}{}
	}

	currentRegistry := filepath.Clean(repo.RegistryPath)
	files := []string{filepath.Join(repo.CommonDir, "frigo", "registry.json")}
	linked, err := filepath.Glob(filepath.Join(repo.CommonDir, "worktrees", "*", "frigo", "registry.json"))
	if err != nil {
		return nil, fmt.Errorf("scan linked worktree registries: %w", err)
	}
	files = append(files, linked...)

	loaded := make(map[string]struct{}, len(files))
	for _, filename := range files {
		filename = filepath.Clean(filename)
		if filename == currentRegistry {
			continue
		}
		if _, ok := loaded[filename]; ok {
			continue
		}
		loaded[filename] = struct{}{}

		reg, err := registry.Load(filename)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("load registry %s: %w", filename, err)
		}
		for _, candidate := range reg.Paths {
			seen[candidate] = struct{}{}
		}
	}

	paths := make([]string, 0, len(seen))
	for candidate := range seen {
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	return paths, nil
}

func rewrite(existing []byte, paths []string) ([]byte, error) {
	block, err := buildBlock(paths)
	if err != nil {
		return nil, err
	}

	prefix, suffix, found, err := splitManagedSection(existing)
	if err != nil {
		return nil, err
	}
	if found {
		if len(block) == 0 {
			return append(prefix, suffix...), nil
		}
		out := make([]byte, 0, len(prefix)+len(block)+len(suffix))
		out = append(out, prefix...)
		out = append(out, block...)
		out = append(out, suffix...)
		return out, nil
	}
	if len(block) == 0 {
		return existing, nil
	}
	if len(existing) == 0 {
		return block, nil
	}

	out := make([]byte, 0, len(existing)+len(block))
	if existing[len(existing)-1] == '\n' {
		out = append(out, existing...)
		out = append(out, block...)
		return out, nil
	}
	// There is no byte sequence that both appends a new line-oriented section
	// after a non-newline-terminated file and restores the prior bytes exactly
	// when the managed section is later removed. Put the managed section first so
	// every user byte remains outside the section unchanged.
	out = append(out, block...)
	out = append(out, existing...)
	return out, nil
}

func buildBlock(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	var builder strings.Builder
	builder.Grow(len(paths) * 8)
	builder.WriteString(startMarker)
	builder.WriteByte('\n')
	for _, candidate := range paths {
		pattern, err := LiteralPattern(candidate)
		if err != nil {
			return nil, fmt.Errorf("build literal pattern for %s: %w", candidate, err)
		}
		builder.WriteString(pattern)
		builder.WriteByte('\n')
	}
	builder.WriteString(endMarker)
	builder.WriteByte('\n')
	return []byte(builder.String()), nil
}

func splitManagedSection(contents []byte) (prefix, suffix []byte, found bool, err error) {
	var before bytes.Buffer
	var after bytes.Buffer
	inside := false
	seen := false
	collectSuffix := false

	for _, chunk := range bytes.SplitAfter(contents, []byte{'\n'}) {
		line := chunk
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}

		switch string(line) {
		case startMarker:
			if inside || seen {
				return nil, nil, false, fmt.Errorf("malformed frigo section in Git exclude file")
			}
			inside = true
			seen = true
		case endMarker:
			if !inside {
				return nil, nil, false, fmt.Errorf("malformed frigo section in Git exclude file")
			}
			inside = false
			collectSuffix = true
		default:
			if inside {
				continue
			}
			if collectSuffix {
				after.Write(chunk)
			} else {
				before.Write(chunk)
			}
		}
	}

	if inside {
		return nil, nil, false, fmt.Errorf("unterminated frigo section in Git exclude file")
	}
	if !seen {
		return contents, nil, false, nil
	}
	return before.Bytes(), after.Bytes(), true, nil
}
