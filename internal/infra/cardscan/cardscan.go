// Package cardscan holds the shared directory scan for task cards. Callers own
// the card format and pass their own parser, reader and sort order. The scan
// owns only the directory walk, the .md filter and the strict policy.
package cardscan

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FaultKind names the step that failed. Strict callers use it to keep their own
// error text and exit codes.
type FaultKind int

const (
	// FaultDir means the tasks directory could not be listed.
	FaultDir FaultKind = iota
	// FaultRead means one card file could not be read.
	FaultRead
	// FaultParse means the parser rejected one card file.
	FaultParse
)

// Fault carries the failing path and the original error.
type Fault struct {
	Kind FaultKind
	Path string
	Err  error
}

func (f *Fault) Error() string { return f.Err.Error() }

func (f *Fault) Unwrap() error { return f.Err }

// ReadCards lists the .md files in dir, reads each with readFile and parses it
// with parse. The result is ordered by less. With strict set, a read or parse
// fault stops the scan. Without strict, the faulty file is skipped. A fault on
// the directory itself is always returned.
func ReadCards[T any](dir string, readFile func(string) ([]byte, error), parse func(string, []byte) (T, error), less func(a, b T) bool, strict bool) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, &Fault{Kind: FaultDir, Path: dir, Err: err}
	}
	cards := make([]T, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := readFile(path)
		if err != nil {
			if strict {
				return nil, &Fault{Kind: FaultRead, Path: path, Err: err}
			}
			continue
		}
		card, err := parse(path, data)
		if err != nil {
			if strict {
				return nil, &Fault{Kind: FaultParse, Path: path, Err: err}
			}
			continue
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return less(cards[i], cards[j]) })
	return cards, nil
}
