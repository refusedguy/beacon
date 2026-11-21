package taskstore

import (
	"os"

	"github.com/refusedguy/beacon/internal/infra/observe"
)

// FileAdapter is the markdown side of the dual-read. Calls go through it so
// every access to the legacy source is counted in beacon metrics.
type FileAdapter struct{}

// ReadFile returns the bytes of path and counts one file adapter read. An
// attempt counts even when the read fails, because a failed access still means
// the file path is in use.
func (FileAdapter) ReadFile(path string) ([]byte, error) {
	observe.FileAdapterRead()
	return os.ReadFile(path)
}
