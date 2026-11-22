package ignore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/roie/frigo/internal/atomicfile"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testsync"
)

const (
	startMarker = "# >>> frigo >>>"
	endMarker   = "# <<< frigo <<<"
)

var syncBeforeReplace = func() error {
	return testsync.Point(context.Background(), "exclude-before-replace")
}

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

// Check reports whether frigo's managed section in the common info/exclude
// file exactly matches the live registry union without changing the file.
func Check(repo repository.Repository, owned registry.Registry) (bool, error) {
	if err := validateExcludePath(repo); err != nil {
		return false, fmt.Errorf("validate Git exclude path: %w", err)
	}
	paths, err := unionPaths(repo, owned)
	if err != nil {
		return false, fmt.Errorf("collect frigo exclude paths: %w", err)
	}

	info, err := os.Lstat(repo.ExcludePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect Git exclude file: %w", err)
	}
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return false, fmt.Errorf("Git exclude file %s is not a regular file", repo.ExcludePath)
	}

	existing, err := os.ReadFile(repo.ExcludePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read Git exclude file: %w", err)
	}
	output, err := rewrite(existing, paths)
	if err != nil {
		return false, err
	}
	return bytes.Equal(existing, output), nil
}

// Sync rewrites frigo's managed section in the common info/exclude file.
func Sync(repo repository.Repository, owned registry.Registry) error {
	if err := validateExcludePath(repo); err != nil {
		return fmt.Errorf("validate Git exclude path: %w", err)
	}
	if err := testsync.Fail("exclude-sync"); err != nil {
		return err
	}
	paths, err := unionPaths(repo, owned)
	if err != nil {
		return fmt.Errorf("collect frigo exclude paths: %w", err)
	}

	existing, err := os.ReadFile(repo.ExcludePath)
	existed := err == nil
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
	if err := syncBeforeReplace(); err != nil {
		return fmt.Errorf("synchronize Git exclude replacement: %w", err)
	}
	current, currentErr := os.ReadFile(repo.ExcludePath)
	currentExists := currentErr == nil
	if currentErr != nil && !os.IsNotExist(currentErr) {
		return fmt.Errorf("compare Git exclude file before replacement: %w", currentErr)
	}
	if currentExists != existed || !bytes.Equal(current, existing) {
		return fmt.Errorf("Git exclude file changed while Frigo was synchronizing it; retry the operation")
	}
	if err := atomicfile.Write(repo.ExcludePath, output, mode); err != nil {
		return fmt.Errorf("write Git exclude file: %w", err)
	}
	return nil
}

func validateExcludePath(repo repository.Repository) error {
	commonDir := filepath.Clean(repo.CommonDir)
	excludePath := filepath.Clean(repo.ExcludePath)
	relative, err := filepath.Rel(commonDir, excludePath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Git exclude file %s is outside common Git directory %s", excludePath, commonDir)
	}

	components := append([]string{commonDir}, strings.Split(relative, string(filepath.Separator))...)
	current := components[0]
	for index, component := range components {
		if index > 0 {
			current = filepath.Join(current, component)
		}
		info, statErr := os.Lstat(current)
		final := index == len(components)-1
		if os.IsNotExist(statErr) && final {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect Git exclude path component %s: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Git exclude path component %s is a symlink", current)
		}
		if final {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("Git exclude file %s is not a regular file", current)
			}
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("Git exclude parent %s is not a directory", current)
		}
	}
	return nil
}

func unionPaths(repo repository.Repository, owned registry.Registry) ([]string, error) {
	seen := make(map[string]struct{}, len(owned.Paths))
	for _, candidate := range owned.Paths {
		seen[candidate] = struct{}{}
	}

	currentRegistry := filepath.Clean(repo.RegistryPath)
	var files []string
	commonStoreExists, err := realDirectoryExists(repo.CommonFrigoDir)
	if err != nil {
		return nil, fmt.Errorf("inspect common frigo store: %w", err)
	}
	if commonStoreExists {
		files = append(files, filepath.Join(repo.CommonFrigoDir, "registry.json"))
	}
	linked, err := liveLinkedRegistryPaths(repo)
	if err != nil {
		return nil, err
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

		exists, err := regularFileExists(filename)
		if err != nil {
			return nil, fmt.Errorf("inspect agreed registry %s: %w", filename, err)
		}
		if !exists {
			continue
		}
		reg, err := registry.Load(filename)
		if err != nil {
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

func liveLinkedRegistryPaths(repo repository.Repository) ([]string, error) {
	adminRoot := filepath.Join(repo.CommonDir, "worktrees")
	if ok, err := realDirectoryExists(adminRoot); err != nil || !ok {
		return nil, err
	}
	if ok, err := realDirectoryExists(repo.CommonFrigoDir); err != nil || !ok {
		return nil, err
	}
	entries, err := os.ReadDir(adminRoot)
	if err != nil {
		return nil, fmt.Errorf("scan active Git worktree administration: %w", err)
	}

	var paths []string
	livePointers := make(map[string]string)
	for _, entry := range entries {
		admin := filepath.Join(adminRoot, entry.Name())
		if ok, _ := realDirectoryExists(admin); !ok {
			continue
		}
		checkoutGitFile, ok := readPathFile(filepath.Join(admin, "gitdir"), "")
		if !ok {
			continue
		}
		checkoutGitFile = resolveRelationshipPath(admin, checkoutGitFile)
		if filepath.Base(checkoutGitFile) != ".git" {
			continue
		}
		checkoutRoot := filepath.Clean(filepath.Dir(checkoutGitFile))
		adminFromCheckout, ok := readPathFile(checkoutGitFile, "gitdir: ")
		if !ok {
			continue
		}
		adminFromCheckout = resolveRelationshipPath(checkoutRoot, adminFromCheckout)
		if adminFromCheckout != filepath.Clean(admin) {
			continue
		}

		pointerPath := filepath.Join(admin, "frigo-id")
		_, statErr := os.Lstat(pointerPath)
		if os.IsNotExist(statErr) {
			claimedStore, err := storeClaimingCheckout(repo.LinkedStoresDir, checkoutRoot)
			if err != nil {
				return nil, err
			}
			if claimedStore != "" && !(repo.LinkedWorktree && filepath.Clean(repo.Root) == checkoutRoot && filepath.Dir(filepath.Clean(repo.RegistryPath)) == claimedStore) {
				return nil, fmt.Errorf("live worktree %s has a missing frigo pointer; run frigo doctor --repair there", checkoutRoot)
			}
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect frigo pointer %s: %w", pointerPath, statErr)
		}
		if exists, err := regularFileExists(pointerPath); err != nil || !exists {
			return nil, fmt.Errorf("invalid frigo pointer %s", pointerPath)
		}
		id, err := metadata.LoadPointer(pointerPath)
		if err != nil {
			return nil, fmt.Errorf("load frigo pointer %s: %w", pointerPath, err)
		}
		if other, duplicate := livePointers[id]; duplicate {
			return nil, fmt.Errorf("live worktrees %s and %s point to the same frigo store %s", other, checkoutRoot, id)
		}
		livePointers[id] = checkoutRoot
		store := filepath.Join(repo.LinkedStoresDir, id)
		if ok, err := realDirectoryExists(store); err != nil || !ok {
			return nil, fmt.Errorf("missing or invalid linked frigo store %s", store)
		}
		manifestPath := filepath.Join(store, "manifest.json")
		if exists, err := regularFileExists(manifestPath); err != nil || !exists {
			return nil, fmt.Errorf("missing or invalid linked frigo manifest %s", manifestPath)
		}
		manifest, err := metadata.Load(manifestPath)
		if err != nil || manifest.ID != id {
			return nil, fmt.Errorf("invalid linked frigo manifest %s", manifestPath)
		}
		if filepath.Clean(manifest.WorktreePath) != checkoutRoot {
			claimedStore, err := storeClaimingCheckout(repo.LinkedStoresDir, checkoutRoot)
			if err != nil {
				return nil, err
			}
			if claimedStore != "" {
				return nil, fmt.Errorf("live worktree %s points to %s but is claimed by frigo store %s", checkoutRoot, store, claimedStore)
			}
		}
		registryPath := filepath.Join(store, "registry.json")
		if exists, err := regularFileExists(registryPath); err != nil {
			return nil, fmt.Errorf("inspect linked frigo registry %s: %w", registryPath, err)
		} else if !exists {
			if _, err := os.Lstat(registryPath); os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("invalid linked frigo registry %s", registryPath)
		}
		paths = append(paths, registryPath)
	}
	return paths, nil
}

func storeClaimingCheckout(storesDir, checkoutRoot string) (string, error) {
	if ok, err := realDirectoryExists(storesDir); err != nil || !ok {
		return "", err
	}
	entries, err := os.ReadDir(storesDir)
	if err != nil {
		return "", fmt.Errorf("scan linked frigo stores: %w", err)
	}
	var claimed string
	for _, entry := range entries {
		store := filepath.Join(storesDir, entry.Name())
		if ok, _ := realDirectoryExists(store); !ok {
			continue
		}
		manifestPath := filepath.Join(store, "manifest.json")
		if ok, _ := regularFileExists(manifestPath); !ok {
			continue
		}
		manifest, err := metadata.Load(manifestPath)
		if err == nil && manifest.ID == entry.Name() && filepath.Clean(manifest.WorktreePath) == checkoutRoot {
			if claimed != "" {
				return "", fmt.Errorf("multiple linked frigo stores claim worktree %s", checkoutRoot)
			}
			claimed = store
		}
	}
	return claimed, nil
}

func realDirectoryExists(filename string) (bool, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	return true, nil
}

func regularFileExists(filename string) (bool, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	return true, nil
}

func readPathFile(filename, prefix string) (string, bool) {
	if exists, _ := regularFileExists(filename); !exists {
		return "", false
	}
	contents, err := os.ReadFile(filename)
	if err != nil || len(contents) == 0 || contents[len(contents)-1] != '\n' {
		return "", false
	}
	value := string(contents[:len(contents)-1])
	if strings.ContainsAny(value, "\r\n") || !strings.HasPrefix(value, prefix) {
		return "", false
	}
	value = strings.TrimPrefix(value, prefix)
	if value == "" {
		return "", false
	}
	return value, true
}

func resolveRelationshipPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
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
