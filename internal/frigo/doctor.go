package frigo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/roie/frigo/internal/ignore"
	"github.com/roie/frigo/internal/lockfile"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testsync"
)

// DoctorOptions controls bounded doctor repairs. Diagnosis is read-only unless
// Repair is true.
type DoctorOptions struct {
	Repair bool
}

// DoctorIssue describes one deterministic diagnosis finding.
type DoctorIssue struct {
	Code       string
	Path       string
	Message    string
	Repairable bool
}

// DoctorAction describes one bounded repair planned or applied by doctor.
type DoctorAction struct {
	Code        string
	Path        string
	Description string
}

// DoctorResult contains the final diagnosis and any repair plan and actions.
type DoctorResult struct {
	Issues  []DoctorIssue
	Planned []DoctorAction
	Applied []DoctorAction
}

type doctorStore struct {
	path     string
	id       string
	manifest metadata.Manifest
	valid    bool
}

type doctorPointerEvidence struct {
	path         string
	checkoutRoot string
	live         bool
}

type doctorState struct {
	repo            repository.Repository
	selected        *doctorStore
	registry        registry.Registry
	hasRegistry     bool
	stores          []doctorStore
	pointerEvidence map[string][]doctorPointerEvidence
	issues          []DoctorIssue
}

// Doctor diagnoses Frigo metadata while holding the common operation lock. If
// repair is requested, it builds the complete plan before applying any action.
func (w *Workspace) Doctor(ctx context.Context, options DoctorOptions) (DoctorResult, error) {
	return w.DoctorWithPlan(ctx, options, nil)
}

// DoctorWithPlan is Doctor with a callback invoked with the complete sorted
// plan while the common lock is held and before the first mutation. The CLI
// uses the callback to guarantee that users see the complete plan first.
func (w *Workspace) DoctorWithPlan(ctx context.Context, options DoctorOptions, beforeApply func([]DoctorAction) error) (result DoctorResult, err error) {
	operationLock, acquireErr := lockfile.Acquire(ctx, w.repo.OperationLockPath, "doctor", w.lockWait)
	if acquireErr != nil {
		result.Issues = []DoctorIssue{{
			Code: "operation-lock-unavailable",
			Path: w.repo.OperationLockPath,
			Message: fmt.Sprintf(
				"%v; verify that no Frigo process is still running for this repository, then manually delete %s; doctor will never remove this lock",
				acquireErr,
				w.repo.OperationLockPath,
			),
		}}
		return result, nil
	}
	defer func() {
		if releaseErr := operationLock.Release(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release operation lock: %w", releaseErr))
		}
	}()

	state := w.diagnoseDoctorLocked(ctx)
	result.Issues = sortedDoctorIssues(state.issues)
	if !options.Repair {
		return result, nil
	}

	result.Planned = buildDoctorPlan(result.Issues)
	if beforeApply != nil {
		if err := beforeApply(append([]DoctorAction(nil), result.Planned...)); err != nil {
			return result, err
		}
	}
	for _, action := range result.Planned {
		changed, applyErr := w.applyDoctorAction(ctx, action)
		if changed {
			result.Applied = append(result.Applied, action)
		}
		if applyErr != nil {
			issues, diagnosisErr := w.rerunDoctorDiagnosisLocked(ctx)
			result.Issues = issues
			return result, errors.Join(
				fmt.Errorf("apply doctor action %s at %s: %w", action.Code, action.Path, applyErr),
				wrapOptional("rerun doctor diagnosis", diagnosisErr),
			)
		}
	}
	issues, diagnosisErr := w.rerunDoctorDiagnosisLocked(ctx)
	result.Issues = issues
	if diagnosisErr != nil {
		return result, fmt.Errorf("rerun doctor diagnosis: %w", diagnosisErr)
	}
	return result, nil
}

func (w *Workspace) rerunDoctorDiagnosisLocked(ctx context.Context) ([]DoctorIssue, error) {
	if err := testsync.Fail("doctor-rediagnosis"); err != nil {
		return nil, err
	}
	return sortedDoctorIssues(w.diagnoseDoctorLocked(ctx).issues), nil
}

func (w *Workspace) diagnoseDoctorLocked(ctx context.Context) doctorState {
	state := doctorState{repo: w.repo}
	w.inspectUnsupportedLinkedState(&state)

	state.stores = w.inspectDoctorStores(&state)
	state.pointerEvidence = w.inspectDoctorPointerRoots()
	for _, store := range state.stores {
		if !store.valid {
			continue
		}
		evidence := state.pointerEvidence[store.id]
		if len(evidence) != 1 || !evidence[0].live || evidence[0].checkoutRoot != store.manifest.WorktreePath {
			state.add("orphan-store", store.path, "stable store has no unique exact live checkout, admin, pointer, and manifest association", false)
		}
	}

	if w.repo.LinkedWorktree {
		state.selectLinkedDoctorStore()
	} else {
		state.selectMainDoctorStore()
	}
	if state.selected == nil {
		return state
	}

	selectedRepo := w.repo.WithFrigoDir(state.selected.path)
	state.repo = selectedRepo
	incomplete := state.inspectSelectedStore(ctx, w)
	if w.repo.LinkedWorktree && incomplete {
		state.add("stable-initialization-incomplete", state.selected.path, "stable store initialization is incomplete", false)
	}
	return state
}

func (w *Workspace) inspectDoctorStores(state *doctorState) []doctorStore {
	for _, directory := range []string{w.repo.CommonFrigoDir, w.repo.LinkedStoresDir} {
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			state.add("metadata-malformed", directory, fmt.Sprintf("inspect stable-store parent: %v", err), false)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			state.add("metadata-malformed", directory, "stable-store parent is not a managed directory", false)
			return nil
		}
	}

	entries, err := os.ReadDir(w.repo.LinkedStoresDir)
	if err != nil {
		state.add("metadata-malformed", w.repo.LinkedStoresDir, fmt.Sprintf("scan stable stores: %v", err), false)
		return nil
	}

	stores := make([]doctorStore, 0, len(entries))
	for _, entry := range entries {
		if !isMetadataID(entry.Name()) {
			continue
		}
		storePath := filepath.Join(w.repo.LinkedStoresDir, entry.Name())
		store := doctorStore{path: storePath, id: entry.Name()}
		info, statErr := os.Lstat(storePath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			state.add("metadata-malformed", storePath, "stable store is not a managed directory", false)
			stores = append(stores, store)
			continue
		}
		manifestPath := filepath.Join(storePath, manifestName)
		data, ok := inspectDoctorMetadataFile(state, manifestPath, "manifest")
		if !ok {
			stores = append(stores, store)
			continue
		}
		manifest, loadErr := metadata.Load(manifestPath)
		if loadErr != nil {
			state.add("metadata-malformed", manifestPath, loadErr.Error(), false)
			stores = append(stores, store)
			continue
		}
		if bytes.Contains(data, []byte("\ufffd")) {
			state.add("metadata-replacement-character", manifestPath, "manifest contains Unicode replacement character U+FFFD", false)
		}
		store.manifest = manifest
		if manifest.ID != entry.Name() {
			state.add("association-id-mismatch", manifestPath, fmt.Sprintf("manifest ID %s does not match store ID %s", manifest.ID, entry.Name()), false)
			stores = append(stores, store)
			continue
		}
		store.valid = true
		stores = append(stores, store)
	}
	return stores
}

func inspectDoctorMetadataFile(state *doctorState, filename, label string) ([]byte, bool) {
	info, err := os.Lstat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			state.add("metadata-malformed", filename, label+" is missing", false)
		} else {
			state.add("metadata-malformed", filename, fmt.Sprintf("inspect %s: %v", label, err), false)
		}
		return nil, false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		state.add("metadata-malformed", filename, label+" is not a regular file", false)
		return nil, false
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		state.add("metadata-malformed", filename, fmt.Sprintf("read %s: %v", label, err), false)
		return nil, false
	}
	if !utf8.Valid(data) {
		state.add("metadata-invalid-utf8", filename, label+" is not valid UTF-8", false)
		return nil, false
	}
	if strings.ContainsRune(string(data), '\ufffd') {
		state.add("metadata-replacement-character", filename, label+" contains Unicode replacement character U+FFFD", false)
	}
	return data, true
}

func (w *Workspace) inspectUnsupportedLinkedState(state *doctorState) {
	adminRoot := filepath.Join(w.repo.CommonDir, "worktrees")
	entries, err := os.ReadDir(adminRoot)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		state.add("metadata-malformed", adminRoot, fmt.Sprintf("scan active Git worktree administration: %v", err), false)
		return
	}
	for _, entry := range entries {
		adminPath := filepath.Join(adminRoot, entry.Name())
		info, statErr := os.Lstat(adminPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		if _, ok := activeDoctorCheckout(adminPath); !ok {
			continue
		}
		legacy := filepath.Join(adminPath, "frigo")
		if _, err := os.Lstat(legacy); err == nil {
			state.add("unsupported-pre-v0.2", legacy, "active linked worktree has unsupported pre-v0.2 metadata", false)
		} else if !os.IsNotExist(err) {
			state.add("metadata-malformed", legacy, fmt.Sprintf("inspect unsupported metadata: %v", err), false)
		}
	}
}

func (w *Workspace) inspectDoctorPointerRoots() map[string][]doctorPointerEvidence {
	evidenceByID := make(map[string][]doctorPointerEvidence)
	adminRoot := filepath.Join(w.repo.CommonDir, "worktrees")
	entries, err := os.ReadDir(adminRoot)
	if err != nil {
		return evidenceByID
	}
	for _, entry := range entries {
		adminPath := filepath.Join(adminRoot, entry.Name())
		info, statErr := os.Lstat(adminPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		pointerPath := filepath.Join(adminPath, "frigo-id")
		id, ok := loadDoctorPointer(pointerPath)
		if !ok {
			continue
		}
		evidence := doctorPointerEvidence{path: pointerPath}
		if checkoutRoot, ok := activeDoctorCheckout(adminPath); ok {
			evidence.checkoutRoot = checkoutRoot
			evidence.live = true
		}
		evidenceByID[id] = append(evidenceByID[id], evidence)
	}
	return evidenceByID
}

func activeDoctorCheckout(adminPath string) (string, bool) {
	checkoutGitFile, ok := readDoctorRelationshipPath(filepath.Join(adminPath, "gitdir"), "")
	if !ok {
		return "", false
	}
	checkoutGitFile = resolveDoctorRelationshipPath(adminPath, checkoutGitFile)
	if filepath.Base(checkoutGitFile) != ".git" {
		return "", false
	}
	checkoutRoot := filepath.Clean(filepath.Dir(checkoutGitFile))
	adminFromCheckout, ok := readDoctorRelationshipPath(checkoutGitFile, "gitdir: ")
	if !ok || resolveDoctorRelationshipPath(checkoutRoot, adminFromCheckout) != filepath.Clean(adminPath) {
		return "", false
	}
	return checkoutRoot, true
}

func readDoctorRelationshipPath(filename, prefix string) (string, bool) {
	info, err := os.Lstat(filename)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	contents, err := os.ReadFile(filename)
	if err != nil || !utf8.Valid(contents) || len(contents) == 0 || contents[len(contents)-1] != '\n' {
		return "", false
	}
	value := string(contents[:len(contents)-1])
	if strings.ContainsAny(value, "\r\n") || !strings.HasPrefix(value, prefix) {
		return "", false
	}
	value = strings.TrimPrefix(value, prefix)
	return value, value != ""
}

func resolveDoctorRelationshipPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func loadDoctorPointer(filename string) (string, bool) {
	info, err := os.Lstat(filename)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	id, err := metadata.LoadPointer(filename)
	return id, err == nil
}

func (state *doctorState) selectLinkedDoctorStore() {
	pointerPath := state.repo.WorktreeIDPath
	info, err := os.Lstat(pointerPath)
	if os.IsNotExist(err) {
		matches := state.storesForRoot(state.repo.Root)
		switch len(matches) {
		case 1:
			if len(state.pointerEvidence[matches[0].id]) != 0 {
				state.add("association-ambiguous", pointerPath, "stable store has conflicting live or stale admin-pointer evidence", false)
				return
			}
			state.selected = &matches[0]
			state.add("association-pointer-missing", pointerPath, "one stable store unambiguously claims this worktree", true)
		case 0:
		default:
			state.add("association-ambiguous", pointerPath, "multiple stable stores claim this worktree", false)
		}
		return
	}
	if err != nil {
		state.add("metadata-malformed", pointerPath, fmt.Sprintf("inspect pointer: %v", err), false)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		state.add("metadata-malformed", pointerPath, "pointer is not a regular file", false)
		return
	}
	data, readErr := os.ReadFile(pointerPath)
	if readErr != nil {
		state.add("metadata-malformed", pointerPath, fmt.Sprintf("read pointer: %v", readErr), false)
		return
	}
	if !utf8.Valid(data) {
		state.add("metadata-invalid-utf8", pointerPath, "pointer is not valid UTF-8", false)
		return
	}
	if bytes.Contains(data, []byte("\ufffd")) {
		state.add("metadata-replacement-character", pointerPath, "pointer contains Unicode replacement character U+FFFD", false)
	}
	id, loadErr := metadata.LoadPointer(pointerPath)
	if loadErr != nil {
		state.add("metadata-malformed", pointerPath, loadErr.Error(), false)
		return
	}
	for i := range state.stores {
		store := &state.stores[i]
		if store.id != id {
			continue
		}
		if !store.valid {
			return
		}
		if store.manifest.WorktreePath != state.repo.Root {
			state.add("association-worktree-mismatch", filepath.Join(store.path, manifestName), "manifest worktreePath does not match the current worktree", false)
			return
		}
		state.selected = store
		return
	}
	state.add("association-store-missing", pointerPath, fmt.Sprintf("pointer references missing or invalid store %s", id), false)
	matches := state.storesForRoot(state.repo.Root)
	if len(matches) == 1 {
		if len(state.pointerEvidence[matches[0].id]) != 0 {
			state.add("association-ambiguous", pointerPath, "stable store has conflicting live or stale admin-pointer evidence", false)
			return
		}
		state.add("association-pointer-mismatch", pointerPath, "one different stable store unambiguously claims this worktree", true)
		state.selected = &matches[0]
	}
}

func (state *doctorState) hasRepairableAction(action DoctorAction) bool {
	for _, issue := range state.issues {
		if issue.Repairable && issue.Code == action.Code && filepath.Clean(issue.Path) == filepath.Clean(action.Path) {
			return true
		}
	}
	return false
}

func (state *doctorState) storesForRoot(root string) []doctorStore {
	var matches []doctorStore
	for _, store := range state.stores {
		if store.valid && store.manifest.WorktreePath == root {
			matches = append(matches, store)
		}
	}
	return matches
}

func (state *doctorState) selectMainDoctorStore() {
	info, err := os.Lstat(state.repo.CommonFrigoDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		state.add("metadata-malformed", state.repo.CommonFrigoDir, "main Frigo store is not a managed directory", false)
		return
	}
	entries, readErr := os.ReadDir(state.repo.CommonFrigoDir)
	if readErr != nil {
		state.add("metadata-malformed", state.repo.CommonFrigoDir, fmt.Sprintf("inspect main Frigo store: %v", readErr), false)
		return
	}
	if len(entries) == 0 || (len(entries) == 1 && entries[0].Name() == "worktrees" && entries[0].IsDir()) {
		return
	}
	state.selected = &doctorStore{path: state.repo.CommonFrigoDir, valid: true}
}

func (state *doctorState) inspectSelectedStore(ctx context.Context, w *Workspace) bool {
	incomplete := false
	registryPath := state.repo.RegistryPath
	if data, ok := inspectDoctorMetadataFile(state, registryPath, "registry"); ok {
		owned, err := registry.Load(registryPath)
		if err != nil {
			state.add("metadata-malformed", registryPath, err.Error(), false)
		} else {
			state.registry = owned
			state.hasRegistry = true
			if strings.ContainsRune(string(data), '\ufffd') {
				state.add("metadata-replacement-character", registryPath, "registry contains Unicode replacement character U+FFFD", false)
			}
		}
	} else {
		incomplete = true
	}

	historyValid := state.validHistory(ctx, w)
	if !historyValid {
		incomplete = true
	}
	attributesRepairable := historyValid && state.hasRegistry
	if !state.exactAttribute(state.repo.AttributesPath, nil, "attributes-public", attributesRepairable) {
		incomplete = true
	}
	if !state.exactAttribute(state.repo.PrivateAttributesPath, []byte(privateAttributes), "attributes-private", attributesRepairable) {
		incomplete = true
	}

	if state.hasRegistry {
		synchronized, err := ignore.Check(state.repo, state.registry)
		if err != nil {
			state.add("exclusions-malformed", state.repo.ExcludePath, err.Error(), false)
		} else if !synchronized {
			state.add("exclusions-stale", state.repo.ExcludePath, "managed exclusions do not match live Frigo registries", true)
		}
	}
	if state.repo.LinkedWorktree && state.selected.manifest.ID != "" && state.hasRegistry {
		state.inspectLifecycleLock(ctx, w)
	}
	return incomplete
}

func (state *doctorState) validHistory(ctx context.Context, w *Workspace) bool {
	history := state.repo.HistoryDir
	info, err := os.Lstat(history)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		state.add("history-invalid", history, "private history is missing or is not a managed directory", false)
		return false
	}
	if err := rejectSymlinksUnder(history); err != nil {
		state.add("history-invalid", history, err.Error(), false)
		return false
	}
	for _, required := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(history, required)); err != nil {
			state.add("history-invalid", history, fmt.Sprintf("private history is missing %s", required), false)
			return false
		}
	}
	bare, err := w.git.Output(ctx, "", "--git-dir="+history, "rev-parse", "--is-bare-repository")
	if err != nil || !strings.EqualFold(bare, "true") {
		state.add("history-invalid", history, "private history is not a valid bare Git repository", false)
		return false
	}
	if err := requireEmptyManagedDirectory(state.repo.HooksDir); err != nil {
		state.add("history-invalid", history, fmt.Sprintf("invalid hooks directory: %v", err), false)
		return false
	}
	for _, config := range [][2]string{
		{"core.hooksPath", state.repo.HooksDir},
		{"core.attributesFile", state.repo.AttributesPath},
		{"core.autocrlf", "false"},
		{"commit.gpgSign", "false"},
	} {
		value, err := w.git.Output(ctx, "", "--git-dir="+history, "config", "--get", config[0])
		if err != nil || value != config[1] {
			state.add("history-invalid", history, fmt.Sprintf("private history config %s is not exact", config[0]), false)
			return false
		}
	}
	return true
}

func (state *doctorState) exactAttribute(filename string, expected []byte, code string, repairable bool) bool {
	info, err := os.Lstat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			state.add(code, filename, "required exact attributes file is missing", repairable)
		} else {
			state.add(code, filename, fmt.Sprintf("inspect attributes: %v", err), false)
		}
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		state.add(code, filename, "attributes path is not a regular file", false)
		return false
	}
	contents, err := os.ReadFile(filename)
	if err != nil {
		state.add(code, filename, fmt.Sprintf("read attributes: %v", err), false)
		return false
	}
	if !bytes.Equal(contents, expected) {
		state.add(code, filename, "attributes contents are not exact", repairable)
		return false
	}
	return true
}

func (state *doctorState) inspectLifecycleLock(ctx context.Context, w *Workspace) {
	lockPath := filepath.Join(state.repo.GitDir, "locked")
	info, err := os.Lstat(lockPath)
	if err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		contents, readErr := os.ReadFile(lockPath)
		if readErr == nil && !utf8.Valid(contents) {
			state.add("metadata-invalid-utf8", lockPath, "lifecycle lock reason is not valid UTF-8", false)
			return
		}
		if readErr == nil && strings.ContainsRune(string(contents), '\ufffd') {
			state.add("metadata-replacement-character", lockPath, "lifecycle lock reason contains Unicode replacement character U+FFFD", false)
		}
	}
	inspector := *w
	inspector.repo = state.repo
	lock, inspectErr := inspector.inspectWorktreeLock(ctx)
	if inspectErr != nil {
		state.add("lifecycle-lock-foreign", lockPath, inspectErr.Error(), false)
		return
	}
	expected := worktreeLockReason(state.selected.manifest.ID)
	active := len(state.registry.Paths) > 0
	if !active {
		if !state.selected.manifest.LockOwned {
			if lock.exists {
				state.add("lifecycle-lock-foreign", lockPath, "inactive registry has a lock that Frigo does not own", false)
			}
			return
		}
		if lock.exists && !lock.matches(expected) {
			state.add("lifecycle-lock-foreign", lockPath, "recorded owned lifecycle lock is foreign, mismatched, or noncanonical", false)
			return
		}
		state.add("lifecycle-lock-stale", lockPath, "inactive linked registry retains stale exact lock ownership", true)
		return
	}
	if !lock.exists {
		state.add("lifecycle-lock-missing", lockPath, "active linked registry is missing its owned lifecycle lock", state.selected.manifest.LockOwned)
		return
	}
	if !lock.matches(expected) {
		state.add("lifecycle-lock-foreign", lockPath, "lifecycle lock is foreign, mismatched, or noncanonical", false)
		return
	}
	if !state.selected.manifest.LockOwned {
		state.add("lifecycle-lock-unowned", lockPath, "exact Frigo lifecycle lock is not recorded as owned", true)
	}
}

func (state *doctorState) add(code, path, message string, repairable bool) {
	for _, issue := range state.issues {
		if issue.Code == code && issue.Path == path {
			return
		}
	}
	state.issues = append(state.issues, DoctorIssue{Code: code, Path: path, Message: message, Repairable: repairable})
}

func sortedDoctorIssues(issues []DoctorIssue) []DoctorIssue {
	result := append([]DoctorIssue(nil), issues...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func buildDoctorPlan(issues []DoctorIssue) []DoctorAction {
	associationBlocked := false
	for _, issue := range issues {
		if issue.Code == "unsupported-pre-v0.2" {
			return nil
		}
		if issue.Code == "history-invalid" || issue.Code == "metadata-malformed" || issue.Code == "metadata-invalid-utf8" {
			associationBlocked = true
		}
	}

	actions := make([]DoctorAction, 0, len(issues))
	for _, issue := range issues {
		if associationBlocked && strings.HasPrefix(issue.Code, "association-pointer-") {
			continue
		}
		if !issue.Repairable {
			continue
		}
		description := map[string]string{
			"association-pointer-missing":  "write the unambiguous stable-store pointer",
			"association-pointer-mismatch": "replace the pointer with the unambiguous stable-store ID",
			"attributes-private":           "restore exact private attributes",
			"attributes-public":            "restore exact public attributes",
			"exclusions-stale":             "synchronize managed Git exclusions",
			"lifecycle-lock-missing":       "restore the recorded owned lifecycle lock",
			"lifecycle-lock-stale":         "clear stale exact lifecycle lock ownership",
			"lifecycle-lock-unowned":       "record ownership of the exact Frigo lifecycle lock",
		}[issue.Code]
		if description == "" {
			continue
		}
		actions = append(actions, DoctorAction{Code: issue.Code, Path: issue.Path, Description: description})
	}
	priority := func(code string) int {
		switch {
		case strings.HasPrefix(code, "attributes-"):
			return 0
		case code == "exclusions-stale":
			return 1
		case strings.HasPrefix(code, "association-pointer-"):
			return 2
		default:
			return 3
		}
	}
	sort.Slice(actions, func(i, j int) bool {
		if priority(actions[i].Code) != priority(actions[j].Code) {
			return priority(actions[i].Code) < priority(actions[j].Code)
		}
		if actions[i].Code != actions[j].Code {
			return actions[i].Code < actions[j].Code
		}
		return actions[i].Path < actions[j].Path
	})
	return actions
}

func (w *Workspace) applyDoctorAction(ctx context.Context, action DoctorAction) (bool, error) {
	state := w.diagnoseDoctorLocked(ctx)
	if !state.hasRepairableAction(action) {
		return false, nil
	}
	if w.doctorActionHook != nil {
		if err := w.doctorActionHook(action.Code); err != nil {
			return false, err
		}
	}
	switch action.Code {
	case "association-pointer-missing", "association-pointer-mismatch":
		matches := state.storesForRoot(w.repo.Root)
		if len(matches) != 1 || len(state.pointerEvidence[matches[0].id]) != 0 {
			return false, fmt.Errorf("pointer association is no longer unambiguous")
		}
		if action.Code == "association-pointer-missing" {
			if err := savePointerExclusive(w.repo.WorktreeIDPath, matches[0].id); err != nil {
				return false, err
			}
			return true, nil
		}
		info, err := os.Lstat(w.repo.WorktreeIDPath)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("existing pointer is not a regular file")
		}
		currentID, err := metadata.LoadPointer(w.repo.WorktreeIDPath)
		if err != nil {
			return false, fmt.Errorf("existing pointer is malformed: %w", err)
		}
		if currentID == matches[0].id {
			return false, nil
		}
		if err := metadata.SavePointer(w.repo.WorktreeIDPath, matches[0].id); err != nil {
			return false, err
		}
		return true, nil
	case "attributes-public", "attributes-private":
		if state.selected == nil {
			return false, fmt.Errorf("current Frigo store is not exactly selected")
		}
		selectedRepo := w.repo.WithFrigoDir(state.selected.path)
		expectedPath := selectedRepo.AttributesPath
		expected := []byte(nil)
		if action.Code == "attributes-private" {
			expectedPath = selectedRepo.PrivateAttributesPath
			expected = []byte(privateAttributes)
		}
		if filepath.Clean(action.Path) != filepath.Clean(expectedPath) {
			return false, fmt.Errorf("attributes action path changed")
		}
		changed, err := ensureManagedFile(expectedPath, expected, 0o600, true)
		if err != nil {
			return false, err
		}
		if !changed {
			contents, err := os.ReadFile(expectedPath)
			if err != nil {
				return false, fmt.Errorf("read attributes repair postcondition: %w", err)
			}
			if !bytes.Equal(contents, expected) {
				return false, fmt.Errorf("attributes repair postcondition failed")
			}
		}
		return changed, nil
	case "exclusions-stale":
		if state.selected == nil || !state.hasRegistry {
			return false, fmt.Errorf("current registry is not exactly selected")
		}
		if err := ignore.Sync(state.repo, state.registry); err != nil {
			return false, err
		}
		return true, nil
	case "lifecycle-lock-missing", "lifecycle-lock-unowned":
		if state.selected == nil || state.selected.manifest.ID == "" {
			return false, fmt.Errorf("linked association is not exact")
		}
		protected := *w
		protected.repo = state.repo
		created, err := protected.ensureWorktreeProtection(ctx, state.selected.manifest.ID)
		if err != nil {
			return false, err
		}
		if action.Code == "lifecycle-lock-missing" && !created {
			return false, fmt.Errorf("missing lifecycle lock was not created")
		}
		return true, nil
	case "lifecycle-lock-stale":
		if state.selected == nil || state.selected.manifest.ID == "" || !state.hasRegistry || len(state.registry.Paths) != 0 {
			return false, fmt.Errorf("stale lifecycle ownership is no longer exactly proven")
		}
		protected := *w
		protected.repo = state.repo
		manifest, err := protected.proveLinkedAssociation(ctx, state.selected.manifest.ID)
		if err != nil {
			return false, err
		}
		if !manifest.LockOwned {
			return false, fmt.Errorf("manifest no longer records lifecycle lock ownership")
		}
		lock, err := protected.inspectWorktreeLock(ctx)
		if err != nil {
			return false, err
		}
		if lock.exists {
			if !lock.matches(worktreeLockReason(manifest.ID)) {
				return false, fmt.Errorf("lifecycle lock is no longer exactly owned")
			}
			if err := protected.releaseOwnedWorktreeLock(ctx); err != nil {
				return false, err
			}
			return true, nil
		}
		if err := protected.persistLockOwnership(manifest, false, "doctor-stale-lock-before-owned-save", "doctor-stale-lock-owned-save"); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported repair action")
	}
}
