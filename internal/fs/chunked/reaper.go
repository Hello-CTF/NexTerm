package chunked

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const stagingMarker = ".nexterm-"

// ParseStagingName reports whether name is a chunked-upload staging file and,
// when it is, the upload ID and staging kind ("manifest", "commit", or
// "part-<index>").
func ParseStagingName(name string) (uploadID, kind string, ok bool) {
	if !strings.HasPrefix(name, ".") {
		return "", "", false
	}
	rest := name[1:]
	marker := strings.LastIndex(rest, stagingMarker)
	if marker <= 0 {
		return "", "", false
	}
	id, kind, found := strings.Cut(rest[marker+len(stagingMarker):], ".")
	if !found || !ids.Valid(id) {
		return "", "", false
	}
	switch {
	case kind == "manifest" || kind == "commit":
		return id, kind, true
	case strings.HasPrefix(kind, "part-"):
		if _, err := strconv.ParseInt(kind[len("part-"):], 10, 64); err != nil {
			return "", "", false
		}
		return id, kind, true
	}
	return "", "", false
}

// ReapReport summarizes what ReapStaleStaging removed.
type ReapReport struct {
	Removed []string
	Kept    int
}

// ReapStaleStaging removes chunked-upload staging files (parts, manifests,
// and commit temporaries) left behind by crashed sessions. Staging is grouped
// by upload ID, and a group is only removed once every file in it is older
// than minAge, so in-flight uploads are never reaped.
func ReapStaleStaging(ctx context.Context, destination base.FileSystem, dir string, minAge time.Duration) (ReapReport, error) {
	entries, err := destination.List(ctx, dir)
	if err != nil {
		return ReapReport{}, err
	}
	type stagingFile struct {
		path    string
		modTime time.Time
	}
	groups := map[string][]stagingFile{}
	var report ReapReport
	for _, entry := range entries {
		if entry.Kind != base.FileFile {
			continue
		}
		uploadID, _, ok := ParseStagingName(entry.Name)
		if !ok {
			continue
		}
		groups[uploadID] = append(groups[uploadID], stagingFile{path: entry.Path, modTime: entry.ModTime})
	}
	for _, files := range groups {
		newest := files[0].modTime
		for _, file := range files[1:] {
			if file.modTime.After(newest) {
				newest = file.modTime
			}
		}
		if minAge > 0 && time.Since(newest) < minAge {
			report.Kept += len(files)
			continue
		}
		sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
		for _, file := range files {
			if err := destination.Delete(ctx, file.path, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return report, fmt.Errorf("reap staging file %s: %w", file.path, err)
			}
			report.Removed = append(report.Removed, file.path)
		}
	}
	sort.Strings(report.Removed)
	return report, nil
}
