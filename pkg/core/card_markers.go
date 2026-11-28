package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// cardMarkerPrefixes are the line starts that git writes for an unresolved
// conflict. A card carrying any of them can fail YAML parsing and drop out of
// dispatch, so callers check a corpus before publishing or merging it.
var cardMarkerPrefixes = [...]string{"<<<<<<<", "=======", ">>>>>>>"}

// CheckCardMarkers walks the markdown files under root and reports the first
// conflict marker it finds. The returned path is the file that holds the
// marker and line is its one-based line number. found is false and both values
// are empty when no marker is present. The helper reads the files it walks and
// does not change them; unreadable paths are skipped.
func CheckCardMarkers(root string) (path string, line int, found bool) {
	_ = filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			return nil
		}
		text := strings.ReplaceAll(string(data), "\r", "")
		for i, raw := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(raw)
			for _, prefix := range cardMarkerPrefixes {
				if strings.HasPrefix(trimmed, prefix) {
					path, line, found = name, i+1, true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return path, line, found
}
