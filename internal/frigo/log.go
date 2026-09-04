package frigo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/roie/frigo/internal/git"
)

const (
	commitRecordFields = 10
	commitRecordFormat = "%H%x00%P%x00%s%x00%b%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI"
)

// LogOptions selects a bounded traversal from one commit in Frigo history.
type LogOptions struct {
	Revision string
	MaxCount *int
	Skip     *int
}

func (w *Workspace) Log(ctx context.Context) (string, error) {
	var result string
	err := w.withLock(ctx, "log", func() error {
		var err error
		result, err = w.logLocked(ctx)
		return err
	})
	return result, err
}

// LogPorcelain returns fixed ten-field commit records terminated by NUL.
func (w *Workspace) LogPorcelain(ctx context.Context, options LogOptions) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "log", func() error {
		var err error
		result, err = w.logPorcelainLocked(ctx, options)
		return err
	})
	return result, err
}

func (w *Workspace) logLocked(ctx context.Context) (string, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return "", err
	}
	base, err := w.resolveHistoryBase(ctx)
	if err != nil {
		return "", err
	}
	if !base.Exists {
		return "no saved history", nil
	}
	output, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "log", "--oneline", "--decorate", base.OID)
	if err != nil {
		return "", fmt.Errorf("read frigo log: %w", err)
	}
	return output, nil
}

func (w *Workspace) logPorcelainLocked(ctx context.Context, options LogOptions) ([]byte, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return nil, err
	}
	if err := validateLogOptions(options); err != nil {
		return nil, err
	}
	start, exists, err := w.resolveLogStart(ctx, options.Revision)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	client := w.git.WithEnv(
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PAGER=cat",
	)
	selection := logSelectionArgs(options, start)
	selected, err := w.privateOutput(ctx, client, append([]string{"rev-list"}, selection...)...)
	if err != nil {
		return nil, fmt.Errorf("select frigo history: %w", err)
	}
	oids, err := parseSelectedOIDs(selected, len(start))
	if err != nil {
		return nil, err
	}
	if len(oids) == 0 {
		return nil, nil
	}
	if err := w.validateRawCommits(ctx, client, oids); err != nil {
		return nil, err
	}

	args := []string{
		"-c", "i18n.logOutputEncoding=UTF-8",
		"-c", "log.showSignature=false",
		"log", "-z", "--no-decorate", "--format=" + commitRecordFormat,
	}
	args = append(args, selection...)
	output, err := w.privateOutputBytes(ctx, client, args...)
	if err != nil {
		return nil, fmt.Errorf("read frigo history records: %w", err)
	}
	if err := validateCommitRecords(output, oids); err != nil {
		return nil, err
	}
	return output, nil
}

func validateLogOptions(options LogOptions) error {
	if options.MaxCount != nil && *options.MaxCount < 0 {
		return errors.New("frigo log max-count must be nonnegative")
	}
	if options.Skip != nil && *options.Skip < 0 {
		return errors.New("frigo log skip must be nonnegative")
	}
	return nil
}

func (w *Workspace) resolveLogStart(ctx context.Context, revision string) (string, bool, error) {
	if revision != "" {
		oid, err := w.resolveCommit(ctx, revision)
		return oid, err == nil, err
	}
	base, err := w.resolveHistoryBase(ctx)
	if err != nil {
		return "", false, err
	}
	return base.OID, base.Exists, nil
}

func logSelectionArgs(options LogOptions, start string) []string {
	args := make([]string, 0, 3)
	if options.MaxCount != nil {
		args = append(args, "--max-count="+strconv.Itoa(*options.MaxCount))
	}
	if options.Skip != nil {
		args = append(args, "--skip="+strconv.Itoa(*options.Skip))
	}
	return append(args, start, "--")
}

func parseSelectedOIDs(output string, oidLength int) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	oids := strings.Split(output, "\n")
	for _, oid := range oids {
		if len(oid) != oidLength || !isHexObjectID(oid) {
			return nil, fmt.Errorf("invalid object ID in frigo history selection %q", oid)
		}
	}
	return oids, nil
}

func isHexObjectID(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}

func (w *Workspace) validateRawCommits(ctx context.Context, client git.Client, oids []string) error {
	input := []byte(strings.Join(oids, "\n") + "\n")
	output, err := w.privateOutputBytesWithInput(ctx, client, input, "cat-file", "--batch")
	if err != nil {
		return fmt.Errorf("read raw frigo commits: %w", err)
	}
	return validateCommitBatch(output, oids)
}

func validateCommitBatch(output []byte, oids []string) error {
	offset := 0
	for _, expectedOID := range oids {
		headerEnd := bytes.IndexByte(output[offset:], '\n')
		if headerEnd < 0 {
			return errors.New("invalid frigo commit batch header")
		}
		headerEnd += offset
		header := strings.Fields(string(output[offset:headerEnd]))
		if len(header) != 3 || header[0] != expectedOID || header[1] != "commit" {
			return fmt.Errorf("invalid frigo commit batch entry %q", output[offset:headerEnd])
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 {
			return fmt.Errorf("invalid frigo commit size %q", header[2])
		}
		objectStart := headerEnd + 1
		if objectStart >= len(output) || size > len(output)-objectStart-1 {
			return fmt.Errorf("truncated frigo commit object %s", expectedOID)
		}
		objectEnd := objectStart + size
		if output[objectEnd] != '\n' {
			return fmt.Errorf("invalid separator after frigo commit object %s", expectedOID)
		}
		if bytes.IndexByte(output[objectStart:objectEnd], 0) >= 0 {
			return fmt.Errorf("frigo commit %s contains a NUL byte", expectedOID)
		}
		offset = objectEnd + 1
	}
	if offset != len(output) {
		return errors.New("unexpected data after frigo commit batch")
	}
	return nil
}

func validateCommitRecords(output []byte, oids []string) error {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return errors.New("invalid frigo commit record termination")
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	if len(fields) != len(oids)*commitRecordFields {
		return fmt.Errorf("invalid frigo commit record field count %d", len(fields))
	}
	for index, oid := range oids {
		if string(fields[index*commitRecordFields]) != oid {
			return fmt.Errorf("frigo commit record %d has an unexpected object ID", index)
		}
	}
	return nil
}
