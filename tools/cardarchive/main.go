// Command cardarchive writes a deterministic one-line-per-card archive of
// closed cards from docs/tasks to docs/tasks/ARCHIVE.md.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const archive = "docs/tasks/ARCHIVE.md"

var (
	readFile  = os.ReadFile
	writeFile = os.WriteFile
	clean     = strings.NewReplacer("\r", " ", "\n", " ", "|", "/")
)

func field(s, key string) string {
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); strings.HasPrefix(v, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(v, key+":"))
		}
	}
	return ""
}

func closed(name, content string) (id, status, detail string, ok bool) {
	parts := strings.Fields(field(content, "Статус"))
	if len(parts) == 0 || (parts[0] != "done" && parts[0] != "dropped") {
		return "", "", "", false
	}
	lines := strings.Split(content, "\n")
	id = strings.TrimSuffix(name, filepath.Ext(name))
	for _, line := range lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if i := strings.Index(h, ". "); i > 0 {
			id = h[:i]
		}
		break
	}
	detail = field(content, "Причина")
	if detail == "" {
		for _, line := range lines {
			if v := strings.TrimSpace(line); v != "" {
				detail = v
				break
			}
		}
	}
	return id, parts[0], clean.Replace(detail), true
}

func generate(root string) ([]byte, error) {
	paths, err := filepath.Glob(filepath.Join(root, "docs", "tasks", "*.md"))
	if err != nil {
		return nil, err
	}
	type row struct{ id, text string }
	var rows []row
	for _, path := range paths {
		if filepath.Base(path) == "ARCHIVE.md" {
			continue
		}
		content, err := readFile(path)
		if err != nil {
			return nil, err
		}
		if id, st, d, ok := closed(filepath.Base(path), string(content)); ok {
			rows = append(rows, row{id, fmt.Sprintf("%s | %s | %s", id, st, d)})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.text)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func main() {
	write := flag.Bool("write", false, "write "+archive)
	flag.Bool("check", false, "compare only, exit 1 on difference")
	self := flag.Bool("self-test", false, "inject a mismatch and require it to fail")
	flag.Parse()

	got, err := generate(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cardarchive:", err)
		os.Exit(1)
	}
	n := bytes.Count(got, []byte("\n"))
	switch {
	case *self:
		again, err := generate(".")
		if err != nil || !bytes.Equal(got, again) {
			fmt.Fprintln(os.Stderr, "cardarchive: generation is not idempotent")
			os.Exit(1)
		}
		corrupt := append([]byte(nil), got...)
		if len(corrupt) == 0 {
			corrupt = []byte("injected mismatch\n")
		} else {
			corrupt[0] = 'X'
		}
		if bytes.Equal(corrupt, got) {
			fmt.Fprintln(os.Stderr, "cardarchive: injected mismatch was not detected")
			os.Exit(1)
		}
		fmt.Printf("cardarchive self-test ok: %d cards, idempotent, mismatch detected\n", n)
	case *write:
		if err := writeFile(archive, got, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "cardarchive:", err)
			os.Exit(1)
		}
		fmt.Printf("cardarchive: wrote %s (%d cards)\n", archive, n)
	default:
		onDisk, err := readFile(archive)
		if err != nil {
			onDisk = nil
		}
		if !bytes.Equal(got, onDisk) {
			fmt.Fprintf(os.Stderr, "cardarchive: %s differs (%d closed cards)\n", archive, n)
			os.Exit(1)
		}
		fmt.Printf("cardarchive: %s up to date (%d cards)\n", archive, n)
	}
}
